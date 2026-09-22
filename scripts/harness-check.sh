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
# AI 세션이 전제로 읽는 문서 전부가 대상이다. .claude/rules 만 보면 Cursor 가 읽는
# .cursor/rules 와 구조 문서 docs/ 의 드리프트를 통째로 놓친다 (이슈 #565).
for d in .claude/rules .cursor/rules docs; do
  [ -d "$d" ] || continue
  while IFS= read -r f; do HARNESS_DOCS+=("$f"); done < <(find "$d" -name '*.md' | sort)
done

# ── 1. 문서가 언급한 저장소 경로의 실재 여부 ───────────────────────────────
# 코드 블록 안의 예시 경로 (foo/bar 등) 와 목표 상태 서술을 구분할 수 없으므로,
# 경고로만 보고한다 — 판단은 사람이.
# 스캔 대상 path family — 저장소 최상위의 디렉토리에서 동적으로 만든다.
# 하드코딩된 6종(internal|pkg|cmd|scripts|deployments|configs)만 보면 문서가 참조하는
# docs/ · .github/ · .claude/ 경로의 드리프트를 통째로 놓친다 (CodeRabbit 피드백).
PATH_FAMILIES=$(find . -maxdepth 1 -mindepth 1 -type d -printf '%f\n' \
  | grep -vE '^(\.git|bin|vendor|node_modules)$' | sort | paste -sd'|')

echo "── 1. 문서가 언급한 경로 실재 확인"

# 오탐 제외 — 문법 설명용 placeholder, Go 심볼 표기, 캐시 경로 등.
is_placeholder() {
  case "$1" in
    */foo/*|*/foo|*/bar/*|*/bar|pkg/mod) return 0 ;;
  esac
  # "core.CrawlerError" 처럼 경로가 아니라 Go 심볼을 가리키는 표기
  [[ "$1" =~ \.[A-Z] ]] && return 0
  # "pkg/agent/Agent", "internal/classifier/Handler" — 마지막 세그먼트가 대문자로 시작하고
  # 확장자가 없으면 파일이 아니라 타입/인터페이스 이름이다.
  [[ "${1##*/}" =~ ^[A-Z][A-Za-z0-9]*$ ]] && return 0
  return 1
}

# 같은 줄에서 "없다 / 미구현 / 목표" 로 이미 부재를 밝힌 경우는 정상 서술.
declared_absent() {
  # -A2: 부재를 밝히는 문구가 줄바꿈으로 다음 줄에 걸린 경우까지 인정한다.
  grep -F -A2 "$2" "$1" | grep -qE '미구현|존재하지 않|없습니다|없음|부재|planned|목표|제안|예시'
}

# gitignore 대상은 저장소에 없는 것이 정상 (예: .claude/settings.local.json).
is_ignored() {
  git check-ignore -q "$1" 2>/dev/null
}

# 예시용 코드 블록을 제거한 본문을 출력한다.
#
# 언어 태그가 붙은 블록(```go, ```yaml, ```bash ...)은 **예시** 다 — CI 워크플로 샘플,
# 마이그레이션 파일명, confluent-kafka 스타일 의사코드 같은 것들이 들어 있어 실재 여부를
# 물을 대상이 아니다.
#
# 반면 태그 없는 블록에는 **디렉토리 트리** 가 들어 있고, 이것은 예시가 아니라 저장소 구조의
# 정경(正經)이다. 이전에는 펜스를 일괄 제거해 트리가 통째로 검사에서 빠졌고, 그 결과
# ingestion_lock.go 처럼 이미 개명된 파일이 계속 문서에 남아 있었다 (이슈 #565).
# 따라서 태그 없는 블록은 남긴다.
strip_code_blocks() {
  awk '
    /^[[:space:]]*```/ {
      if (infence) { infence = 0; tagged = 0 }
      else {
        infence = 1
        # ``` 뒤에 언어 태그가 붙어 있으면 예시 블록으로 본다.
        tagged = ($0 ~ /^[[:space:]]*```[[:alnum:]]/)
      }
      next
    }
    !infence || !tagged
  ' "$1"
}

# 디렉토리 트리 블록에서 전체 경로를 복원해 출력한다.
#
# 트리는 파일명만 적는다 — "│   │   ├── ingestion_marker.go" 에는 internal/locks/ 접두사가
# 없어 경로 정규식이 매치하지 않는다. 들여쓰기 깊이로 부모를 추적해 전체 경로를 만든다.
# 루트는 블록 첫 줄(예: "internal/processor/fetcher/" 또는 "issuetracker/").
expand_tree_paths() {
  awk -v families="${PATH_FAMILIES}" '
    BEGIN { n = split(families, fa, "|"); for (i = 1; i <= n; i++) isfamily[fa[i]] = 1 }
    /^[[:space:]]*```[[:alnum:]]/ { skip = 1; next }          # 태그 블록은 통째로 건너뛴다
    /^[[:space:]]*```/ { infence = !infence; if (!infence) skip = 0; delete parent; root = ""; next }
    !infence { next }
    skip { next }

    # 트리 문자가 없는 첫 줄 = 루트 (예: "issuetracker/", "internal/locks/")
    !/[├└│]/ {
      line = $0
      sub(/#.*$/, "", line)
      gsub(/[[:space:]]/, "", line)
      if (line ~ /^[A-Za-z0-9_.][A-Za-z0-9_.\/-]*\/$/) {
        # 루트의 첫 세그먼트가 저장소 최상위 디렉토리면 실제 접두사 (예: "internal/locks/").
        # 아니면 저장소 자신을 가리키는 표기 (예: "issuetracker/") 이므로 벗겨낸다.
        # 디렉토리명 대소문자에 의존하지 않기 위해 이름 비교가 아니라 family 소속으로 판정한다.
        split(line, seg, "/")
        root = (seg[1] in isfamily) ? line : ""
        delete parent
      }
      next
    }

    {
      # "│   │   ├── name" → 접두부 길이로 깊이를 계산 (트리 한 단계 = 4칸)
      match($0, /[├└]── /)
      if (RSTART == 0) next
      depth = int((RSTART - 1) / 4)
      name = substr($0, RSTART + RLENGTH)
      sub(/[[:space:]]*#.*$/, "", name)         # "# 설명" 형식 주석
      sub(/[[:space:]]*←.*$/, "", name)         # "← 설명" 형식 주석 (docs/architecture 관행)
      sub(/[[:space:]]+$/, "", name)
      if (name == "") next

      isdir = (name ~ /\/$/)
      base = name; sub(/\/$/, "", base)

      # 부모 경로 조립
      full = root
      bad = 0
      for (i = 0; i < depth; i++) {
        if (!(i in parent)) { bad = 1; break }
        full = full parent[i] "/"
      }
      if (bad) next

      if (isdir) { parent[depth] = base; for (i = depth + 1; i in parent; i++) delete parent[i] }
      print full base
    }
  ' "$1"
}

missing_paths=0
for doc in "${HARNESS_DOCS[@]}"; do
  [ -f "$doc" ] || continue
  doc_dir=$(dirname "$doc")
  while IFS= read -r p; do
    [ -e "$p" ] && continue
    # docs/architecture/ 의 문서들은 서로를 자기 디렉토리 기준 상대 경로로 링크한다
    # (예: docs/architecture/cmd/issuetracker.md 안의 "internal/scheduler.md" 는
    #  docs/architecture/internal/scheduler.md 를 가리킨다).
    [ -e "$doc_dir/$p" ] && continue
    # docs/architecture/<family>/... 처럼 저장소 구조를 미러링하는 문서 트리도 인정한다.
    [ -e "docs/architecture/$p" ] && continue
    is_placeholder "$p" && continue
    is_ignored "$p" && continue
    declared_absent "$doc" "$p" && continue
    warn "$doc → 존재하지 않는 경로: $p"
    missing_paths=$((missing_paths + 1))
    # test/internal/... 처럼 접두사가 붙은 경로의 중간 매치 방지 — 앞이 / 또는 단어문자면 제외
    # 저장소 최상위 디렉토리를 실제로 훑어 family 목록을 만든다 (CodeRabbit 피드백).
    # 하드코딩하면 docs/ · .github/ · .claude/ 처럼 문서가 실제로 참조하는 경로를 놓친다.
  done < <({ strip_code_blocks "$doc" \
               | grep -ohP "(?<![\w/])(${PATH_FAMILIES})/[\w./-]+"
             expand_tree_paths "$doc" \
               | grep -P "^(${PATH_FAMILIES})/"
           } | sed 's/[.,)`]*$//' | sort -u)
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
done < <(grep -n 'Go 1\.[0-9]\+' "${HARNESS_DOCS[@]}" 2>/dev/null)
grep -q "go-version: '1\." "${HARNESS_DOCS[@]}" 2>/dev/null \
  && fail "규약 문서의 CI 예시가 go-version 을 하드코딩 (go-version-file: go.mod 사용)" \
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

# ── 7. 폐지된 cron loop 자산이 되살아나지 않았는지 ────────────────────────
# 이슈 #548 로 cron loop 방식은 폐지됐다. 파일이 다시 생기거나 규약이 cron 등록을 지시하면
# AI 가 매 PR 마다 cron 을 걸려 하고, 그때마다 사용자가 예외를 지시해야 한다.
echo "── 7. 폐지된 cron loop 자산 확인"
for dead in .claude/loop.md .claude/pr-feedback.md scripts/pr-feedback.sh; do
  [ -e "$dead" ] && fail "폐지된 cron loop 자산이 존재: $dead (이슈 #548)"
done
# 규약이 cron 등록을 "지시" 하는지 — 폐지를 설명하는 문맥은 제외
if grep -hnE 'CronCreate|cron 으로 등록|cron 자동 등록' CLAUDE.md .claude/rules/*.md 2>/dev/null \
     | grep -vE '금지|폐지|구 규약|구 #129|호출하지 않는다|대체' | grep -q .; then
  fail "규약이 여전히 cron 등록을 지시하는 것으로 보임 — CLAUDE.md / 07-workflow 확인"
else
  ok "cron loop 자산 부재 + 규약이 cron 등록을 지시하지 않음"
fi

echo
echo "─────────────────────────────────────"
if [ "$fail_count" -gt 0 ]; then
  echo "${RED}실패 ${fail_count}건${RESET} / 경고 ${warn_count}건"
  exit 1
fi
echo "${GREEN}통과${RESET} (경고 ${warn_count}건)"
