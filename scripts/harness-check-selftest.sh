#!/usr/bin/env bash
# harness-check.sh 의 검출력 자체를 검증한다 (이슈 #565).
#
# harness-check 는 "경고 0건" 을 목표로 하는데, 억제 로직이 과해지면 드리프트를 놓치면서도
# 0건을 유지한다 — 조용히 무력화된다. 실제로 이 일이 있었다: "release 없음" 이라는 무관한
# 설명이 부재 선언으로 오인돼 상위 디렉토리 전체가 면제됐다.
#
# 따라서 알려진 드리프트를 일부러 심어 **검출되는지** 확인한다. 각 케이스는 원본을 백업하고
# 주입 → 검사 → 복원한다. CI 와 로컬 양쪽에서 실행 가능.
set -uo pipefail
cd "$(dirname "$0")/.."

GREEN=$'\e[32m'; RED=$'\e[31m'; RESET=$'\e[0m'
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0

backup() { cp "$1" "$TMP/$(echo "$1" | tr / _)"; }
restore() { cp "$TMP/$(echo "$1" | tr / _)" "$1"; }

# expect_detect <설명> <주입 후 경고에 나타나야 할 문자열> <파일> <sed 표현식>
expect_detect() {
  local desc=$1 needle=$2 file=$3 expr=$4
  backup "$file"
  sed -i "$expr" "$file"
  # harness-check 는 FAIL 이 있으면 exit 1 을 낸다 — 주입 테스트에서는 정상이므로
  # 종료 코드를 삼키고 출력만 본다 (pipefail 하에서 파이프가 끊기는 것을 막는다).
  local out
  out=$(bash scripts/harness-check.sh 2>&1 || true)
  if printf '%s' "$out" | sed 's/\x1b\[[0-9;]*m//g' | grep -q -- "$needle"; then
    printf "%s ok %s %s\n" "$GREEN" "$RESET" "$desc"; pass=$((pass + 1))
  else
    printf "%sFAIL%s %s — 드리프트를 심었는데 검출되지 않음\n" "$RED" "$RESET" "$desc"; fail=$((fail + 1))
  fi
  restore "$file"
}

echo "── harness-check 검출력 자체 검증"

expect_detect "트리 블록의 파일명 드리프트" "ingestion_locker.go" \
  .claude/rules/01-architecture.md 's|├── ingestion_marker.go|├── ingestion_locker.go|'

expect_detect "트리 블록의 디렉토리 드리프트" "internal/workerpools" \
  .claude/rules/01-architecture.md 's|├── workerpool/|├── workerpools/|'

expect_detect "중첩 트리(하위 stage) 드리프트" "processor/validation" \
  .claude/rules/01-architecture.md 's|├── validate/|├── validation/|'

expect_detect "docs/ 상대링크 산문 드리프트" "internal/messagebus" \
  docs/architecture/internal/bus.md 's|(\.\./\.\./\.\./internal/bus/)|(../../../internal/messagebus/)|'

expect_detect "docs/ 표(表) 행의 경로 드리프트" "cmd/adminx" \
  docs/architecture/cmd/README.md 's|cmd/admin/`](../../../cmd/admin/)|cmd/adminx/`](../../../cmd/adminx/)|'

expect_detect "4단계 깊이 상대링크 드리프트" "ingestion_marker_v2.go" \
  docs/architecture/internal/locks/README.md 's|ingestion_marker.go|ingestion_marker_v2.go|g'

expect_detect ".cursor 룰셋의 Go 버전 드리프트" "Go 1.21" \
  .cursor/rules/development-workflow.md 's|- Go 1.24+|- Go 1.21+|'

# 오탐 회귀 — "목표 구조" 로 표시된 트리는 경고를 만들면 안 된다.
printf "     "
baseline=$(bash scripts/harness-check.sh 2>&1 || true)
if printf '%s' "$baseline" | grep -q "docs/en"; then
  printf "%sFAIL%s 목표 구조 블록이 오탐을 만든다\n" "$RED" "$RESET"; fail=$((fail + 1))
else
  printf "%s ok %s 목표 구조 블록은 오탐 없음\n" "$GREEN" "$RESET"; pass=$((pass + 1))
fi

echo "─────────────────────────────────────"
if [ "$fail" -gt 0 ]; then
  printf "%s실패%s (통과 %d / 실패 %d)\n" "$RED" "$RESET" "$pass" "$fail"; exit 1
fi
printf "%s통과%s (%d 케이스)\n" "$GREEN" "$RESET" "$pass"
