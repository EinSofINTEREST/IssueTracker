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


def rt(text: str) -> dict:
    return {"type": "text", "text": {"content": text, "link": None}}


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


def upsert_pr(ds_id: str, pr: dict) -> str:
    props = pr_to_props(pr)
    existing = find_row_by_pr_number(ds_id, pr["number"])
    if existing:
        # 재오픈된 PR 이 archive 되어 있을 수 있으므로 in_trash=False 로 복구
        notion_request(f"/pages/{existing}", "PATCH",
                       {"properties": props, "in_trash": False})
        return f"updated row {existing} (#{pr['number']})"
    res = notion_request("/pages", "POST", {
        "parent": {"data_source_id": ds_id},
        "properties": props,
    })
    return f"created row {res['id']} (#{pr['number']})"


# ---------- GitHub fetching ----------

PR_FIELDS = "number,title,state,url,author,createdAt,mergedAt,closedAt,updatedAt"


def gh_fetch_pr(repo: str, number: int) -> dict:
    out = gh("pr", "view", str(number), "--repo", repo, "--json", PR_FIELDS)
    return json.loads(out)


def gh_list_prs(repo: str, state: str, limit: int = 1000) -> list[dict]:
    out = gh("pr", "list", "--repo", repo, "--state", state,
             "--limit", str(limit), "--json", PR_FIELDS)
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

    log(f"creating {len(ordered)} rows")
    for i, pr in enumerate(ordered, 1):
        notion_request("/pages", "POST", {
            "parent": {"data_source_id": ds_id},
            "properties": pr_to_props(pr),
        })
        if i % 25 == 0 or i == len(ordered):
            log(f"  inserted {i}/{len(ordered)} (latest #{pr['number']})")
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
