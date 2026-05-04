#!/usr/bin/env bash
#
# pr-feedback.sh
#
# 최신 열린 PR 20개를 조회하고, 상태 파일과 비교해 신규 PR 번호를 stdout 에 출력.
# .claude/pr-feedback.md 의 cron loop 에서 사용.
#
# Usage:
#   scripts/pr-feedback.sh
#
# 출력:
#   신규 PR 번호를 한 줄에 하나씩 출력 (없으면 출력 없음)
#
# 상태 파일:
#   /tmp/issuetracker-pr-watch-state.json
#   {"known_prs": [232, 231, 230], "last_run_at": "2026-05-03T..."}

set -euo pipefail

STATE_FILE="/tmp/issuetracker-pr-watch-state.json"

# 최신 열린 PR 20개 조회 (짧은 시간 다수 PR 누락 방지)
latest=$(gh pr list --state open --limit 20 --json number --jq '[.[].number]')

if [[ -z "$latest" || "$latest" == "[]" ]]; then
  exit 0
fi

# 상태 파일에서 기존 known_prs 로드
if [[ -f "$STATE_FILE" ]]; then
  known=$(jq '.known_prs // []' "$STATE_FILE")
else
  known="[]"
fi

# 신규 PR = latest 에 있고 known 에 없는 것
new_prs=$(jq -n --argjson latest "$latest" --argjson known "$known" \
  '$latest - $known | .[]')

# 상태 파일 갱신 (known_prs = latest 20개로 교체)
now=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
tmp=$(mktemp)
jq -n --argjson known_prs "$latest" --arg last_run_at "$now" \
  '{known_prs: $known_prs, last_run_at: $last_run_at}' > "$tmp"
mv "$tmp" "$STATE_FILE"

# 신규 PR 번호 출력 (없으면 출력 생략)
if [[ -n "$new_prs" ]]; then
  echo "$new_prs"
fi
