#!/usr/bin/env bash
# gh-meta.sh — 이슈/PR 의 title prefix 를 읽어 Label · Issue Type 을 자동 부여합니다 (이슈 #238).
#
# 사용법:
#   scripts/gh-meta.sh issue <NUMBER>   # 이슈 label + type 부여
#   scripts/gh-meta.sh pr    <NUMBER>   # PR label 부여 (닫는 이슈의 label 와 동기화)
#
# Issue Type IDs (이 repo 고정값):
#   Feature  → IT_kwDODsDQh84By0jb
#   Bug      → IT_kwDODsDQh84By0ja
#   Task     → IT_kwDODsDQh84By0jZ
#
# Issue prefix → label / type 매핑 (07-workflow.md 규약 6):
#   [FEATURE]  → enhancement  / Feature
#   [FIX]      → bug          / Bug
#   [HOTFIX]   → bug + hotfix / Bug
#   [REFACTOR] → refactor     / Task
#   [CHORE]    → chore        / Task
#   [DOCS]     → documentation / Task

set -euo pipefail

REPO="EinSofINTEREST/IssueTracker"

FEATURE_TYPE_ID="IT_kwDODsDQh84By0jb"
BUG_TYPE_ID="IT_kwDODsDQh84By0ja"
TASK_TYPE_ID="IT_kwDODsDQh84By0jZ"

usage() {
  echo "Usage: $0 issue <NUMBER> | pr <NUMBER>" >&2
  exit 1
}

# prefix → (label, type_id) 반환
resolve_label_and_type() {
  local title="$1"
  local prefix
  prefix=$(echo "$title" | grep -oP '^\[[A-Z]+\]' || true)

  case "$prefix" in
    "[FEATURE]")  echo "enhancement $FEATURE_TYPE_ID" ;;
    "[FIX]")      echo "bug $BUG_TYPE_ID" ;;
    "[HOTFIX]")   echo "bug+hotfix $BUG_TYPE_ID" ;;
    "[REFACTOR]") echo "refactor $TASK_TYPE_ID" ;;
    "[CHORE]")    echo "chore $TASK_TYPE_ID" ;;
    "[DOCS]")     echo "documentation $TASK_TYPE_ID" ;;
    *)
      echo "ERROR: unrecognized prefix '$prefix' in title: $title" >&2
      exit 1
      ;;
  esac
}

apply_issue_label_and_type() {
  local number="$1"
  local title
  title=$(gh issue view "$number" --repo "$REPO" --json title --jq .title)
  echo "Issue #$number: $title"

  local meta
  meta=$(resolve_label_and_type "$title")
  local label="${meta%% *}"
  local type_id="${meta##* }"

  # label 적용 (복수 label 지원 — hotfix 경우 bug+hotfix)
  if [[ "$label" == *"+"* ]]; then
    local l1="${label%%+*}"
    local l2="${label##*+}"
    gh issue edit "$number" --repo "$REPO" --add-label "$l1" --add-label "$l2"
    echo "  label: $l1, $l2"
  else
    gh issue edit "$number" --repo "$REPO" --add-label "$label"
    echo "  label: $label"
  fi

  # Issue Type 적용
  local issue_id
  issue_id=$(gh issue view "$number" --repo "$REPO" --json id --jq .id)
  gh api graphql -f query='
mutation($issueId: ID!, $issueTypeId: ID!) {
  updateIssueIssueType(input: {issueId: $issueId, issueTypeId: $issueTypeId}) {
    issue { number issueType { name } }
  }
}' -f issueId="$issue_id" -f issueTypeId="$type_id" --jq '.data.updateIssueIssueType.issue.issueType.name' \
  | xargs -I{} echo "  type: {}"
}

apply_pr_label() {
  local number="$1"
  local pr_title
  pr_title=$(gh pr view "$number" --repo "$REPO" --json title --jq .title)
  echo "PR #$number: $pr_title"

  # PR 이 닫는 이슈 번호 추출 (Closes #N)
  local closing_issue
  closing_issue=$(gh pr view "$number" --repo "$REPO" --json body --jq '.body' \
    | grep -oiP '(?:closes|fixes|resolves)\s+#\K[0-9]+' | head -1 || true)

  local label
  if [[ -n "$closing_issue" ]]; then
    echo "  closing issue: #$closing_issue"
    local issue_title
    issue_title=$(gh issue view "$closing_issue" --repo "$REPO" --json title --jq .title)
    local meta
    meta=$(resolve_label_and_type "$issue_title")
    label="${meta%% *}"
  else
    # 닫는 이슈가 없으면 PR title prefix 로 직접 추론
    echo "  (no closing issue found, inferring from PR title)"
    local meta
    meta=$(resolve_label_and_type "$pr_title")
    label="${meta%% *}"
  fi

  if [[ "$label" == *"+"* ]]; then
    local l1="${label%%+*}"
    local l2="${label##*+}"
    gh pr edit "$number" --repo "$REPO" --add-label "$l1" --add-label "$l2"
    echo "  label: $l1, $l2"
  else
    gh pr edit "$number" --repo "$REPO" --add-label "$label"
    echo "  label: $label"
  fi
}

[[ $# -lt 2 ]] && usage

SUBCOMMAND="$1"
NUMBER="$2"

case "$SUBCOMMAND" in
  issue) apply_issue_label_and_type "$NUMBER" ;;
  pr)    apply_pr_label "$NUMBER" ;;
  *)     usage ;;
esac
