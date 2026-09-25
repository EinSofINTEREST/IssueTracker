#!/usr/bin/env python3
"""Record a design decision (ADR) in the Notion '설계 결정 기록' database (규약 7, 이슈 #665).

AI 세션은 Notion MCP 로 직접 기록한다. 이 스크립트는 **토큰이 있는 환경** — 사람, CI 의
`@claude` 경로 — 에서 같은 DB 에 같은 형식으로 쓰기 위한 것이다. 수동 curl 대신 항상 이것을
쓴다 (이슈 #243 과 같은 원칙). 요청 헬퍼 · 재시도 정책 · markdown → block 변환은
notion_pr_sync.py 를 그대로 재사용한다 (이슈 #563).

Subcommands:
  add      새 결정 행 + ADR 본문 생성
  status   기존 행의 상태 변경 (폐기 / 대체됨 — 행을 지우지 않는다)

Required env:
  NOTION_API_TOKEN        Notion integration token (workspace bot)
Optional env:
  NOTION_DECISION_DS_ID   data source id (defaults to the repo constant below)

Examples:
  scripts/notion-decision.py add \\
    --title "validate 의 DB 조회 실패는 timeout 만 재시도" \\
    --date 2026-09-24 --type "운영 정책" --status 채택 --area validate,bus \\
    --issues "#648, #603" --pr 649 --decider "AI 자율 (규약 내)" \\
    --summary "DeadlineExceeded 만 재시도, 그 외 DB 에러는 DLQ 유지" \\
    --body-file decision.md

  scripts/notion-decision.py status <page_id> 대체됨

  --dry-run 은 네트워크 없이 보낼 payload 만 출력한다 (토큰 불필요).
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import notion_pr_sync as npr  # noqa: E402  (요청 헬퍼 · rt · md_to_blocks 재사용)

DEFAULT_DS_ID = "8f5b163d-397a-4182-8b14-2546ffb4a16b"

# DB 의 select 옵션과 1:1 — 여기 없는 값은 Notion 이 새 옵션을 만들어 버리므로
# 오타가 조용히 분류를 오염시킨다. 보내기 전에 거른다.
TYPES = ("설계 변경", "의사 결정", "규약", "기술 선택", "운영 정책")
STATUSES = ("제안", "채택", "폐기", "대체됨")
AREAS = ("fetcher", "parser", "validate", "enrich", "bus", "storage", "api",
         "agent", "scoring", "infra", "규약", "기타")
DECIDERS = ("사용자", "AI 제안 · 사용자 승인", "AI 자율 (규약 내)")

# ADR 본문에 있어야 하는 절. 빠지면 경고 — 특히 "검토한 대안" 은 결정의 가치가 있는 곳이다.
ADR_SECTIONS = ("맥락", "결정", "근거", "검토한 대안", "결과", "되돌리기")


def _choice(value: str, allowed: tuple[str, ...], flag: str) -> str:
    if value not in allowed:
        sys.exit(f"{flag}: {value!r} 는 허용값이 아님 — {' / '.join(allowed)}")
    return value


def _areas(raw: str) -> list[str]:
    areas = [a.strip() for a in raw.split(",") if a.strip()]
    if not areas:
        sys.exit("--area: 하나 이상 지정")
    for a in areas:
        _choice(a, AREAS, "--area")
    return areas


def _date(raw: str) -> str:
    try:
        return dt.date.fromisoformat(raw).isoformat()
    except ValueError:
        sys.exit(f"--date: {raw!r} 는 YYYY-MM-DD 가 아님")


def _read_body(args: argparse.Namespace) -> str:
    if args.body_file == "-":
        return sys.stdin.read()
    with open(args.body_file, encoding="utf-8") as f:
        return f.read()


def _warn_missing_sections(body: str) -> None:
    missing = [s for s in ADR_SECTIONS if f"## {s}" not in body]
    if missing:
        # stderr — --dry-run 의 payload(stdout)를 파이프해도 경고가 묻히지 않도록.
        print(f"warning: ADR 절 누락 — {', '.join(missing)} (규약 7 형식 참조)", file=sys.stderr)


def build_props(args: argparse.Namespace) -> dict:
    props = {
        "제목": {"title": [npr.rt(args.title)]},
        "날짜": {"date": {"start": _date(args.date)}},
        "유형": {"select": {"name": _choice(args.type, TYPES, "--type")}},
        "상태": {"select": {"name": _choice(args.status, STATUSES, "--status")}},
        "영역": {"multi_select": [{"name": a} for a in _areas(args.area)]},
        "관련 이슈": {"rich_text": [npr.rt(args.issues)]},
        "결정자": {"select": {"name": _choice(args.decider, DECIDERS, "--decider")}},
        "요약": {"rich_text": [npr.rt(args.summary)]},
    }
    if args.pr is not None:
        props["PR"] = {"number": args.pr}
    return props


def cmd_add(args: argparse.Namespace) -> int:
    ds_id = os.environ.get("NOTION_DECISION_DS_ID", DEFAULT_DS_ID)
    body = _read_body(args)
    _warn_missing_sections(body)
    props = build_props(args)
    blocks = npr.md_to_blocks(body)

    if args.dry_run:
        print(json.dumps({"parent": {"data_source_id": ds_id}, "properties": props,
                          "children": blocks}, ensure_ascii=False, indent=2))
        return 0

    res = npr.notion_request("/pages", "POST", {
        "parent": {"data_source_id": ds_id},
        "properties": props,
    })
    page_id = res["id"]
    # 새 페이지라 지울 블록이 없다 — replace_page_body 의 chunk 처리만 빌려 쓴다.
    npr.replace_page_body(page_id, blocks)
    npr.log(f"created decision {page_id}: {res.get('url', '')}")
    return 0


def cmd_status(args: argparse.Namespace) -> int:
    status = _choice(args.status, STATUSES, "status")
    if args.dry_run:
        print(json.dumps({"page": args.page_id, "properties": {"상태": {"select": {"name": status}}}},
                         ensure_ascii=False))
        return 0
    npr.notion_request(f"/pages/{args.page_id}", "PATCH",
                       {"properties": {"상태": {"select": {"name": status}}}})
    npr.log(f"updated {args.page_id} → 상태={status}")
    return 0


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--dry-run", action="store_true", help="네트워크 없이 payload 만 출력")
    sub = ap.add_subparsers(dest="cmd", required=True)

    a = sub.add_parser("add", help="새 결정 행 생성")
    a.add_argument("--title", required=True)
    a.add_argument("--date", default=dt.date.today().isoformat(), help="결정한 날 (기본 오늘)")
    a.add_argument("--type", required=True, help=" / ".join(TYPES))
    a.add_argument("--status", default="채택", help=" / ".join(STATUSES))
    a.add_argument("--area", required=True, help="쉼표 구분: " + ", ".join(AREAS))
    a.add_argument("--issues", required=True, help='"#658, #603" — 메인 이슈 앞에')
    a.add_argument("--pr", type=int)
    a.add_argument("--decider", required=True, help=" / ".join(DECIDERS))
    a.add_argument("--summary", required=True, help="한 줄 — 무엇을 골랐고 무엇을 버렸는지")
    a.add_argument("--body-file", required=True, help="ADR markdown 파일, '-' 는 stdin")
    a.set_defaults(fn=cmd_add)

    s = sub.add_parser("status", help="기존 행의 상태 변경")
    s.add_argument("page_id")
    s.add_argument("status", help=" / ".join(STATUSES))
    s.set_defaults(fn=cmd_status)

    args = ap.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
