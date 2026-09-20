#!/usr/bin/env bash
# harness-check.sh — AI 에이전트에게 지시를 주입하는 계층이 저장소 실체와 맞는지 대조합니다 (이슈 #539).
#
# 대상:
#   1. 개발 규약 (CLAUDE.md / .claude/rules / .claude/loop.md) 이 언급한 저장소 경로의 실재 여부
#   2. cmd/ 목록 · Go 버전 · 커버리지 임계값이 문서와 일치하는지
#   3. prompt asset 이름·placeholder 계약 (Go 테스트에 위임 — 여기선 asset 개수만 sanity)
#
# 사용법:
#   scripts/harness-check.sh          # 검사만 (실패 시 exit 1)
#
# 왜 필요한가: 규약 문서는 매 세션 AI 에게 전제로 주입된다. 낡은 서술은 곧 잘못된 지시가 되고,
# 구조를 바꾼 PR 이 문서를 같이 고치지 않으면 조용히 어긋난다.

set -uo pipefail

cd "$(dirname "$0")/.."

RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RESET=$'\033[0m'
fail_count=0
warn_count=0

fail() { echo "${RED}FAIL${RESET} $*"; fail_count=$((fail_count + 1)); }
warn() { echo "${YELLOW}WARN${RESET} $*"; warn_count=$((warn_count + 1)); }
ok()   { echo "${GREEN} ok ${RESET} $*"; }

HARNESS_DOCS=(CLAUDE.md .claude/loop.md .claude/pr-feedback.md)
while IFS= read -r f; do HARNESS_DOCS+=("$f"); done < <(find .claude/rules -name '*.md' | sort)

# ── 1. 문서가 언급한 저장소 경로의 실재 여부 ───────────────────────────────
# 코드 블록 안의 예시 경로 (foo/bar 등) 와 목표 상태 서술을 구분할 수 없으므로,
# 경고로만 보고한다 — 판단은 사람이.
echo "── 1. 문서가 언급한 경로 실재 확인"

# 오탐 제외 — 문법 설명용 placeholder, Go 심볼 표기, 캐시 경로 등.
is_placeholder() {
  case "$1" in
    */foo/*|*/foo|*/bar/*|*/bar|pkg/mod) return 0 ;;
  esac
  # "core.CrawlerError" 처럼 경로가 아니라 Go 심볼을 가리키는 표기
  [[ "$1" =~ \.[A-Z] ]] && return 0
  return 1
}

# 같은 줄에서 "없다 / 미구현 / 목표" 로 이미 부재를 밝힌 경우는 정상 서술.
declared_absent() {
  grep -F "$2" "$1" | grep -qE '미구현|존재하지 않|없습니다|없음|부재|planned|목표|제안|예시'
}

missing_paths=0
for doc in "${HARNESS_DOCS[@]}"; do
  [ -f "$doc" ] || continue
  while IFS= read -r p; do
    [ -e "$p" ] && continue
    is_placeholder "$p" && continue
    declared_absent "$doc" "$p" && continue
    warn "$doc → 존재하지 않는 경로: $p"
    missing_paths=$((missing_paths + 1))
    # test/internal/... 처럼 접두사가 붙은 경로의 중간 매치 방지 — 앞이 / 또는 단어문자면 제외
  done < <(grep -ohP '(?<![\w/])(internal|pkg|cmd|scripts|deployments|configs)/[\w./-]+' "$doc" \
             | sed 's/[.,)`]*$//' | sort -u)
done
[ "$missing_paths" -eq 0 ] && ok "문서가 언급한 internal/ pkg/ cmd/ 경로가 모두 실재 (placeholder·부재 명시 제외)"

# ── 2. cmd/ 목록 ──────────────────────────────────────────────────────────
echo "── 2. cmd/ 목록 대조"
actual_cmds=$(find cmd -maxdepth 1 -mindepth 1 -type d -printf '%f\n' | sort | tr '\n' ' ')
for c in $actual_cmds; do
  # 부분 문자열 매칭 금지 — "migrate" 누락을 "migrate-down" 이 가려주면 대조가 무의미해진다.
  # 앞뒤가 단어 경계(또는 줄 끝)인 토큰 전체 일치만 인정.
  grep -qE "(^|[^A-Za-z0-9_-])${c}([^A-Za-z0-9_-]|$)" CLAUDE.md \
    || fail "cmd/$c 가 CLAUDE.md 디렉토리 요약에 없음"
done
# 문서에만 있고 실재하지 않는 cmd 탐지
while IFS= read -r c; do
  [ -d "cmd/$c" ] || fail "CLAUDE.md 가 존재하지 않는 cmd/$c 를 언급"
done < <(grep -oE 'cmd/[a-z-]+' CLAUDE.md | sed 's|cmd/||' | sort -u)
[ "$fail_count" -eq 0 ] && ok "cmd 목록 일치 ($actual_cmds)"

# ── 3. Go 버전 ────────────────────────────────────────────────────────────
echo "── 3. Go 버전 대조"
gomod_ver=$(awk '/^go /{print $2; exit}' go.mod)
gomod_minor=${gomod_ver%.*}
while IFS= read -r line; do
  doc=${line%%:*}; ver=$(grep -oE 'Go 1\.[0-9]+' <<<"$line" | head -1)
  [ -z "$ver" ] && continue
  [ "${ver#Go }" = "$gomod_minor" ] || fail "$doc 의 '$ver' 가 go.mod ($gomod_ver) 와 불일치"
done < <(grep -rn 'Go 1\.[0-9]\+' CLAUDE.md .claude/rules/*.md 2>/dev/null)
grep -rq "go-version: '1\." .claude/rules/*.md 2>/dev/null \
  && fail ".claude/rules 의 CI 예시가 go-version 을 하드코딩 (go-version-file: go.mod 사용)" \
  || ok "Go 버전 서술이 go.mod ($gomod_ver) 와 일치"

# ── 4. 커버리지 임계값 ────────────────────────────────────────────────────
echo "── 4. 커버리지 임계값 대조"
ci_threshold=$(grep -oE 'threshold=[0-9]+' .github/workflows/ci-quality.yml | head -1 | cut -d= -f2)
if [ -z "$ci_threshold" ]; then
  fail "ci-quality.yml 에서 threshold 를 찾지 못함 (변수명이 바뀌었는지 확인)"
elif grep -q "커버리지 ${ci_threshold}% 이상" CLAUDE.md; then
  ok "커버리지 임계값 ${ci_threshold}% 가 CLAUDE.md 와 일치"
else
  fail "CI 임계값은 ${ci_threshold}% 인데 CLAUDE.md 의 '반드시 지킬 규칙' 과 불일치"
fi

# ── 5. status check 이름 ──────────────────────────────────────────────────
echo "── 5. status check 이름 대조"
while IFS= read -r name; do
  grep -q "\`$name\`" docs/ci/status-checks.md \
    || fail "워크플로 job name '$name' 이 docs/ci/status-checks.md 에 없음"
done < <(grep -hE '^\s{4}name: ' .github/workflows/ci-quality.yml .github/workflows/ci-convention.yml \
           | sed 's/^\s*name: //')
ok "status check 이름 대조 완료"

# ── 6. prompt asset sanity ────────────────────────────────────────────────
# 이름·placeholder 계약의 실질 검증은 test/internal/promptcontract 가 담당.
# 여기선 asset 이 사라지지 않았는지만 본다.
echo "── 6. prompt asset sanity"
asset_count=$(find pkg/llm/prompt/assets -name '*.txt' | wc -l)
if [ "$asset_count" -lt 1 ]; then
  fail "prompt asset 이 하나도 없음 — embed 빌드가 깨짐"
else
  ok "prompt asset ${asset_count}개 (계약 검증은 test/internal/promptcontract)"
fi

# ── 6-1. loop 자동 종료 임계값 ────────────────────────────────────────────
echo "── 7. loop 종료 조건 대조"
loop_threshold=$(grep -oE 'idle_streak >= [0-9]+' .claude/loop.md | head -1 | grep -oE '[0-9]+')
if [ -n "$loop_threshold" ] && grep -q "${loop_threshold}회 연속" CLAUDE.md; then
  ok "loop 종료 임계값 ${loop_threshold}회 가 CLAUDE.md 와 일치"
else
  fail "loop.md 임계값(${loop_threshold:-?}회) 과 CLAUDE.md 서술이 불일치"
fi

echo
echo "─────────────────────────────────────"
if [ "$fail_count" -gt 0 ]; then
  echo "${RED}실패 ${fail_count}건${RESET} / 경고 ${warn_count}건"
  exit 1
fi
echo "${GREEN}통과${RESET} (경고 ${warn_count}건)"
