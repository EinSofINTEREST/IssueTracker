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
TMP=$(mktemp -d)
pass=0; fail=0

# 주입한 드리프트는 무슨 일이 있어도 되돌린다.
# rm -rf "$TMP" 만 하면, 중간에 죽었을 때 변조된 문서가 작업 트리에 남고 백업은 사라진다 —
# 커밋 전 미저장 변경이 있었다면 복구할 수단이 없다 (CodeRabbit 피드백).
MUTATED=()
cleanup() {
  local f
  for f in "${MUTATED[@]-}"; do
    [ -n "$f" ] && [ -f "$TMP/$(echo "$f" | tr / _)" ] && cp "$TMP/$(echo "$f" | tr / _)" "$f"
  done
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

backup() {
  # 대상이 없으면 즉시 중단한다 — 주입이 일어나지 않은 채 케이스가 "통과" 하면
  # selftest 가 검출력을 증명하는 것이 아니라 거짓 안심을 준다.
  if [ ! -f "$1" ]; then
    printf "%sFAIL%s selftest 대상 파일이 없음: %s\n" "$RED" "$RESET" "$1" >&2
    exit 1
  fi
  cp "$1" "$TMP/$(echo "$1" | tr / _)"
  MUTATED+=("$1")
}
restore() {
  cp "$TMP/$(echo "$1" | tr / _)" "$1"
  # 복원된 파일은 추적 목록에서 뺀다 — cleanup 이 중복 복사하지 않도록.
  local i out=()
  for i in "${MUTATED[@]-}"; do [ "$i" = "$1" ] || out+=("$i"); done
  MUTATED=("${out[@]-}")
}

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

expect_detect "코드가 참조하는 prompt 이름의 asset 부재" "parser/claude/ghost.user" \
  pkg/agent/claude/contracts.go 's|parser/claude/page.user|parser/claude/ghost.user|'

# 오탐 회귀 — 주석 안의 옛 prompt 이름은 경고를 만들면 안 된다.
# 이력 설명으로 옛 이름을 남기는 일이 흔하다 (실제로 codex/contracts.go 가 그렇다 — 이슈 #594).
printf "     "
backup pkg/agent/claude/contracts.go
printf '\n// 과거 이름: "parser/nonexistent/page.user" — 주석이므로 경고 대상이 아니다.\n' \
  >> pkg/agent/claude/contracts.go
comment_out=$(bash scripts/harness-check.sh 2>&1 || true)
if printf '%s' "$comment_out" | sed 's/\x1b\[[0-9;]*m//g' | grep -q -- "parser/nonexistent/page.user"; then
  printf "%sFAIL%s 주석 안의 prompt 이름이 오탐을 만든다\n" "$RED" "$RESET"; fail=$((fail + 1))
else
  printf "%s ok %s 주석 안의 prompt 이름은 오탐 없음\n" "$GREEN" "$RESET"; pass=$((pass + 1))
fi
restore pkg/agent/claude/contracts.go

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
