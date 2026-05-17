#!/usr/bin/env python3
"""Sync GitHub PRs to the Notion '최근 PR' data source.

Modes (from --mode):
  - event    : single PR upsert/delete driven by pull_request webhook env vars
  - backfill : delete all rows then re-create from GitHub (open + merged)

Required env:
  NOTION_API_TOKEN  Notion integration token (workspace bot)
  GH_TOKEN          GitHub token (defaults to github.token in Actions)
  PR_DS_ID          Notion data source id (defaults to repo constant below)
  REPO              owner/repo (defaults to EinSofINTEREST/IssueTracker)

Event-mode env (set by GitHub Actions pull_request payload):
  PR_NUMBER
  PR_STATE          'open' | 'closed'
  PR_MERGED         'true' | 'false'

Notion DB schema (data source e9b7771f-...):
  제목 (title) · 번호 (number) · 카테고리 (select) · 연결 이슈 (number)
  Merged (date) · URL (url) · 작성자 (rich_text)
  상태 (select: Open / Merged) · Created (date)
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

NOTION_API     = "https://api.notion.com/v1"
NOTION_VERSION = "2025-09-03"
DEFAULT_DS_ID  = "e9b7771f-a2d4-4ada-a8fb-b82df8782f45"
DEFAULT_REPO   = "EinSofINTEREST/IssueTracker"

PREFIX_RE = re.compile(r"^\[([A-Z]+)#(\d+)\]\s*(.*)$")


# ---------- helpers ----------

def log(msg: str) -> None:
    print(msg, flush=True)


def notion_request(path: str, method: str = "GET", body: dict | None = None,
                   token: str | None = None) -> dict:
    token = token or os.environ["NOTION_API_TOKEN"]
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(
        f"{NOTION_API}{path}",
        data=data,
        headers={
            "Authorization": f"Bearer {token}",
            "Notion-Version": NOTION_VERSION,
            "Content-Type": "application/json",
        },
        method=method,
    )
    for attempt in range(4):
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read()
                if not raw:
                    return {}
                return json.loads(raw)
        except urllib.error.HTTPError as e:
            if e.code == 429 and attempt < 3:
                time.sleep(2 ** attempt)
                continue
            body_text = e.read().decode(errors="replace")[:800]
            raise RuntimeError(f"Notion {method} {path} -> {e.code}: {body_text}") from e
        except urllib.error.URLError as e:
            if attempt < 3:
                time.sleep(2 ** attempt)
                continue
            raise


def gh(*args: str) -> str:
    """Run gh CLI and return stdout (raises on failure)."""
    env = os.environ.copy()
    return subprocess.check_output(["gh", *args], env=env, text=True)


def rt(text: str, *, bold: bool = False, italic: bool = False,
       code: bool = False, strike: bool = False,
       link: str | None = None) -> dict:
    return {
        "type": "text",
        "text": {
            "content": text,
            "link": ({"url": link} if link else None),
        },
        "annotations": {
            "bold": bold, "italic": italic, "strikethrough": strike,
            "underline": False, "code": code, "color": "default",
        },
    }


# ---------- markdown -> Notion blocks ----------

NOTION_LANG_MAP = {
    "":     "plain text",
    "txt":  "plain text",
    "text": "plain text",
    "go":   "go",
    "py":   "python",
    "python": "python",
    "js":   "javascript",
    "ts":   "typescript",
    "tsx":  "typescript",
    "jsx":  "javascript",
    "sh":   "shell",
    "bash": "shell",
    "zsh":  "shell",
    "yaml": "yaml",
    "yml":  "yaml",
    "json": "json",
    "diff": "diff",
    "html": "html",
    "css":  "css",
    "sql":  "sql",
    "md":   "markdown",
    "rb":   "ruby",
    "java": "java",
    "c":    "c",
    "cpp":  "c++",
    "rust": "rust",
    "rs":   "rust",
    "toml": "toml",
    "xml":  "xml",
}

_INLINE_RE = re.compile(
    r"\*\*(.+?)\*\*"                          # **bold**
    r"|`([^`\n]+?)`"                           # `code`
    r"|\[([^\]\n]+?)\]\(([^)\s]+?)\)"          # [text](url)
    r"|\*([^*\n]+?)\*"                         # *italic* (after bold)
    r"|_([^_\n]+?)_"                           # _italic_
)


def _truncate(s: str, n: int = 1900) -> str:
    """Notion rich_text content max 2000 chars; leave a small margin."""
    return s if len(s) <= n else s[: n - 1] + "…"


def md_inline(text: str) -> list[dict]:
    """Parse inline markdown into a list of rich_text spans."""
    spans: list[dict] = []
    pos = 0
    for m in _INLINE_RE.finditer(text):
        if m.start() > pos:
            spans.append(rt(_truncate(text[pos : m.start()])))
        if m.group(1) is not None:
            spans.append(rt(_truncate(m.group(1)), bold=True))
        elif m.group(2) is not None:
            spans.append(rt(_truncate(m.group(2)), code=True))
        elif m.group(3) is not None and m.group(4) is not None:
            spans.append(rt(_truncate(m.group(3)), link=m.group(4)))
        elif m.group(5) is not None:
            spans.append(rt(_truncate(m.group(5)), italic=True))
        elif m.group(6) is not None:
            spans.append(rt(_truncate(m.group(6)), italic=True))
        pos = m.end()
    if pos < len(text):
        spans.append(rt(_truncate(text[pos:])))
    return spans or [rt("")]


def _block(t: str, **payload) -> dict:
    return {"object": "block", "type": t, t: payload}


_LIST_RE     = re.compile(r"^[-*+]\s+(.+)$")
_TODO_RE     = re.compile(r"^[-*+]\s+\[([ xX])\]\s+(.+)$")
_NUMLIST_RE  = re.compile(r"^\d+\.\s+(.+)$")
_HEADING_RE  = re.compile(r"^(#{1,3})\s+(.+)$")
_HR_RE       = re.compile(r"^\s*(?:-{3,}|\*{3,}|_{3,})\s*$")


def md_to_blocks(md: str) -> list[dict]:
    """Convert markdown to Notion block list. Subset of CommonMark."""
    lines = md.replace("\r\n", "\n").split("\n")
    blocks: list[dict] = []
    i, n = 0, len(lines)
    while i < n:
        raw = lines[i]
        s = raw.rstrip()
        ls = s.lstrip()

        # code fence (also catches indented fences inside list items)
        if ls.startswith("```"):
            lang = ls[3:].strip().lower()
            i += 1
            code_lines: list[str] = []
            while i < n and not lines[i].lstrip().startswith("```"):
                code_lines.append(lines[i])
                i += 1
            i += 1  # closing fence (or EOF)
            blocks.append(_block(
                "code",
                rich_text=[rt(_truncate("\n".join(code_lines)))],
                language=NOTION_LANG_MAP.get(lang, "plain text"),
            ))
            continue

        # blank line
        if not s.strip():
            i += 1
            continue

        # heading
        m = _HEADING_RE.match(s)
        if m:
            level = len(m.group(1))
            blocks.append(_block(f"heading_{level}", rich_text=md_inline(m.group(2))))
            i += 1
            continue

        # horizontal rule
        if _HR_RE.match(s):
            blocks.append(_block("divider"))
            i += 1
            continue

        # blockquote (collect contiguous `> ` lines)
        if s.startswith(">"):
            q_lines: list[str] = []
            while i < n and lines[i].rstrip().startswith(">"):
                q_lines.append(re.sub(r"^>\s?", "", lines[i].rstrip()))
                i += 1
            blocks.append(_block("quote", rich_text=md_inline("\n".join(q_lines))))
            continue

        # checkbox / bulleted / numbered list (one item per line; nesting not supported)
        m = _TODO_RE.match(s)
        if m:
            checked = m.group(1).lower() == "x"
            blocks.append(_block("to_do", rich_text=md_inline(m.group(2)), checked=checked))
            i += 1
            continue
        m = _LIST_RE.match(s)
        if m:
            blocks.append(_block("bulleted_list_item", rich_text=md_inline(m.group(1))))
            i += 1
            continue
        m = _NUMLIST_RE.match(s)
        if m:
            blocks.append(_block("numbered_list_item", rich_text=md_inline(m.group(1))))
            i += 1
            continue

        # paragraph — join until blank or structural line
        para = [raw]
        i += 1
        while i < n:
            nxt = lines[i].rstrip()
            if not nxt.strip():
                break
            if nxt.lstrip().startswith("```") or nxt.lstrip().startswith(">"):
                break
            if _HEADING_RE.match(nxt) or _HR_RE.match(nxt):
                break
            if _LIST_RE.match(nxt) or _NUMLIST_RE.match(nxt) or _TODO_RE.match(nxt):
                break
            para.append(lines[i])
            i += 1
        text = " ".join(l.strip() for l in para if l.strip())
        if text:
            blocks.append(_block("paragraph", rich_text=md_inline(text)))
    return blocks


def files_to_blocks(files: list[dict], max_files: int = 100) -> list[dict]:
    """Render a list of changed files as bulleted_list_item blocks."""
    blocks: list[dict] = []
    shown = files[:max_files]
    for f in shown:
        path = f.get("path", "?")
        adds = f.get("additions", 0) or 0
        dels = f.get("deletions", 0) or 0
        blocks.append(_block(
            "bulleted_list_item",
            rich_text=[
                rt(path, code=True),
                rt(f"  +{adds} -{dels}"),
            ],
        ))
    if len(files) > max_files:
        blocks.append(_block(
            "paragraph",
            rich_text=[rt(f"… 외 {len(files) - max_files}개 (총 {len(files)}개)", italic=True)],
        ))
    return blocks


def pr_to_body_blocks(pr: dict) -> list[dict]:
    """Compose the full child-block list rendered into a PR row page."""
    blocks: list[dict] = []
    # Header / URL
    blocks.append(_block(
        "callout",
        rich_text=[
            rt(f"#{pr['number']} ", code=True),
            rt(pr.get("title", ""), bold=True),
        ],
        icon={"type": "emoji", "emoji": "🔗"},
        color="gray_background",
    ))
    blocks.append(_block(
        "paragraph",
        rich_text=[rt("GitHub: "), rt(pr["url"], link=pr["url"])],
    ))

    # Body
    body = (pr.get("body") or "").strip()
    blocks.append(_block("divider"))
    blocks.append(_block("heading_2", rich_text=[rt("📝 본문")]))
    if body:
        blocks.extend(md_to_blocks(body))
    else:
        blocks.append(_block(
            "paragraph",
            rich_text=[rt("(본문 없음)", italic=True)],
        ))

    # Files
    files = pr.get("files") or []
    blocks.append(_block("divider"))
    blocks.append(_block("heading_2", rich_text=[rt(f"📁 변경 파일 ({len(files)})")]))
    if files:
        blocks.extend(files_to_blocks(files))
    else:
        blocks.append(_block(
            "paragraph",
            rich_text=[rt("(변경 파일 정보 없음)", italic=True)],
        ))

    return blocks


def split_prefix(title: str) -> tuple[str | None, int | None, str]:
    m = PREFIX_RE.match(title)
    if not m:
        return None, None, title
    return m.group(1), int(m.group(2)), m.group(3)


def pr_to_props(pr: dict) -> dict:
    category, issue_num, body = split_prefix(pr["title"])
    merged = bool(pr.get("mergedAt"))
    notion_state = "Merged" if merged else "Open"
    props: dict = {
        "제목":   {"title": [rt(body or pr["title"])]},
        "번호":   {"number": pr["number"]},
        "URL":    {"url": pr["url"]},
        "작성자":  {"rich_text": [rt((pr.get("author") or {}).get("login") or "")]},
        "Created": {"date": {"start": pr["createdAt"]}},
        "상태":    {"select": {"name": notion_state}},
    }
    if pr.get("mergedAt"):
        props["Merged"] = {"date": {"start": pr["mergedAt"]}}
    else:
        props["Merged"] = {"date": None}
    if category:
        props["카테고리"] = {"select": {"name": category}}
    if issue_num is not None:
        props["연결 이슈"] = {"number": issue_num}
    return props


# ---------- Notion lookups ----------

def find_row_by_pr_number(ds_id: str, number: int) -> str | None:
    payload = {
        "filter": {"property": "번호", "number": {"equals": number}},
        "page_size": 5,
    }
    res = notion_request(f"/data_sources/{ds_id}/query", "POST", payload)
    rows = res.get("results", [])
    return rows[0]["id"] if rows else None


def iter_all_rows(ds_id: str):
    cursor = None
    while True:
        payload = {"page_size": 100}
        if cursor:
            payload["start_cursor"] = cursor
        res = notion_request(f"/data_sources/{ds_id}/query", "POST", payload)
        for r in res.get("results", []):
            yield r["id"]
        if not res.get("has_more"):
            return
        cursor = res.get("next_cursor")


def archive_page(page_id: str) -> None:
    # Notion API 2026-03-11 변경: `archived` 속성이 `in_trash` 로 대체됨
    notion_request(f"/pages/{page_id}", "PATCH", {"in_trash": True})


def replace_page_body(page_id: str, new_blocks: list[dict]) -> None:
    """Replace the entire body (child blocks) of a Notion page.

    Existing children are deleted, then new_blocks are appended in chunks of 100
    (Notion's per-request children cap).
    """
    # 1) collect existing children with pagination
    existing_ids: list[str] = []
    cursor: str | None = None
    while True:
        path = f"/blocks/{page_id}/children?page_size=100"
        if cursor:
            path += f"&start_cursor={cursor}"
        res = notion_request(path, "GET")
        for b in res.get("results", []):
            existing_ids.append(b["id"])
        if not res.get("has_more"):
            break
        cursor = res.get("next_cursor")

    # 2) delete each (DELETE is supported on blocks)
    for bid in existing_ids:
        notion_request(f"/blocks/{bid}", "DELETE")
        time.sleep(0.15)

    # 3) append new blocks, chunked by 100
    for start in range(0, len(new_blocks), 100):
        chunk = new_blocks[start : start + 100]
        notion_request(f"/blocks/{page_id}/children", "PATCH", {"children": chunk})
        time.sleep(0.2)


def upsert_pr(ds_id: str, pr: dict, body_sync: bool = True) -> str:
    props = pr_to_props(pr)
    existing = find_row_by_pr_number(ds_id, pr["number"])
    if existing:
        # 재오픈된 PR 이 archive 되어 있을 수 있으므로 in_trash=False 로 복구
        notion_request(f"/pages/{existing}", "PATCH",
                       {"properties": props, "in_trash": False})
        page_id = existing
        verb = "updated"
    else:
        res = notion_request("/pages", "POST", {
            "parent": {"data_source_id": ds_id},
            "properties": props,
        })
        page_id = res["id"]
        verb = "created"

    if body_sync:
        replace_page_body(page_id, pr_to_body_blocks(pr))

    return f"{verb} row {page_id} (#{pr['number']})"


# ---------- GitHub fetching ----------

PR_FIELDS = "number,title,state,url,author,createdAt,mergedAt,closedAt,updatedAt,body,files"
PR_FIELDS_LIST = "number,title,state,url,author,createdAt,mergedAt,closedAt,updatedAt"


def gh_fetch_pr(repo: str, number: int) -> dict:
    """Full PR view including body + files (used for body sync)."""
    out = gh("pr", "view", str(number), "--repo", repo, "--json", PR_FIELDS)
    return json.loads(out)


def gh_list_prs(repo: str, state: str, limit: int = 1000) -> list[dict]:
    """Lightweight PR list (metadata only — backfill calls gh_fetch_pr per PR for body)."""
    out = gh("pr", "list", "--repo", repo, "--state", state,
             "--limit", str(limit), "--json", PR_FIELDS_LIST)
    return json.loads(out)


# ---------- modes ----------

def mode_event(ds_id: str, repo: str) -> int:
    number = os.environ.get("PR_NUMBER")
    if not number:
        log("event mode: PR_NUMBER not set, nothing to do")
        return 0
    pr = gh_fetch_pr(repo, int(number))
    state = pr.get("state", "").upper()  # OPEN / MERGED / CLOSED
    merged = bool(pr.get("mergedAt"))

    # closed && !merged → delete
    if state == "CLOSED" and not merged:
        existing = find_row_by_pr_number(ds_id, pr["number"])
        if existing:
            archive_page(existing)
            log(f"deleted row for closed-unmerged PR #{pr['number']}")
        else:
            log(f"closed-unmerged PR #{pr['number']} not in DB; skipping")
        return 0

    log(upsert_pr(ds_id, pr))
    return 0


def mode_backfill(ds_id: str, repo: str) -> int:
    # GitHub fetch 를 destructive archive 보다 먼저 — fetch 실패 시 DB 비우지 않도록
    log("== fetching open + merged PRs from GitHub ==")
    prs: dict[int, dict] = {}
    for state in ("open", "merged"):
        for pr in gh_list_prs(repo, state):
            prs[pr["number"]] = pr
    if not prs:
        log("error: GitHub returned 0 PRs; aborting before any destructive operation")
        return 2
    ordered = sorted(prs.values(), key=lambda p: p["number"])
    log(f"fetched {len(ordered)} PRs from GitHub")

    log("== archiving all existing rows ==")
    n_archived = 0
    for row_id in iter_all_rows(ds_id):
        archive_page(row_id)
        n_archived += 1
        time.sleep(0.2)
    log(f"archived {n_archived} rows")

    log(f"creating {len(ordered)} rows (each fetches body + files)")
    for i, pr_lite in enumerate(ordered, 1):
        # Fetch full body + files per PR (list endpoint omits them).
        pr_full = gh_fetch_pr(repo, pr_lite["number"])
        res = notion_request("/pages", "POST", {
            "parent": {"data_source_id": ds_id},
            "properties": pr_to_props(pr_full),
        })
        replace_page_body(res["id"], pr_to_body_blocks(pr_full))
        if i % 10 == 0 or i == len(ordered):
            log(f"  inserted {i}/{len(ordered)} (latest #{pr_full['number']})")
        time.sleep(0.2)
    log("backfill complete")
    return 0


# ---------- entry ----------

def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", choices=["event", "backfill"], required=True)
    ap.add_argument("--ds-id", default=os.environ.get("PR_DS_ID", DEFAULT_DS_ID))
    ap.add_argument("--repo",  default=os.environ.get("REPO", DEFAULT_REPO))
    args = ap.parse_args(argv)

    if "NOTION_API_TOKEN" not in os.environ:
        log("error: NOTION_API_TOKEN not set")
        return 2

    if args.mode == "event":
        return mode_event(args.ds_id, args.repo)
    return mode_backfill(args.ds_id, args.repo)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
