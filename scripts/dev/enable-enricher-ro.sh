#!/usr/bin/env bash
# 이슈 #472 — dev / CI 환경에서 enricher_ro 계정에 LOGIN + PASSWORD 를 부여하는 헬퍼.
#
# migration 031 은 NOLOGIN 으로만 role 을 생성합니다 (자격증명을 git 에 commit 하지 않기 위함).
# 본 스크립트는 .env 의 ENRICHER_DB_RO_PASSWORD 값을 읽어 dev DB 의 role 에 LOGIN 권한과
# 비밀번호를 부여합니다. 운영 환경은 별도 secret 관리 도구로 ALTER ROLE 을 수행하세요.
#
# 사용:
#   scripts/dev/enable-enricher-ro.sh                 # .env 의 값 사용
#   scripts/dev/enable-enricher-ro.sh <password>      # 명시적 password
#
# 사전 조건:
#   - migration 031 이 적용되어 enricher_ro 가 존재
#   - $POSTGRES_USER / $POSTGRES_PASSWORD 가 superuser 권한
#
# 멱등 — 여러 번 실행해도 동일 결과.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

# .env 로딩 (있을 때만 — production 에서 본 스크립트는 사용하지 말 것).
if [[ -f "$REPO_ROOT/.env" ]]; then
  set -a
  # shellcheck disable=SC1090,SC1091
  source "$REPO_ROOT/.env"
  set +a
fi

PASSWORD="${1:-${ENRICHER_DB_RO_PASSWORD:-}}"
if [[ -z "$PASSWORD" ]]; then
  echo "ERROR: password not provided. set ENRICHER_DB_RO_PASSWORD in .env or pass as arg." >&2
  exit 1
fi

HOST="${POSTGRES_HOST:-localhost}"
PORT="${POSTGRES_PORT:-5432}"
DB="${POSTGRES_DB:-main}"
ADMIN_USER="${POSTGRES_USER:-postgres}"
ADMIN_PW="${POSTGRES_PASSWORD:-postgres}"

# Password 가 SQL injection 친화적이지 않도록 작은 따옴표 escape.
ESCAPED="${PASSWORD//\'/\'\'}"

# stdin 으로 SQL 을 전달 — `-c` 플래그를 쓰면 password 가 process listing (ps) 에 노출
# 되므로 회피 (PR #473 coderabbit minor 지적).
printf "ALTER ROLE enricher_ro WITH LOGIN PASSWORD '%s';" "$ESCAPED" | \
  PGPASSWORD="$ADMIN_PW" psql -h "$HOST" -p "$PORT" -U "$ADMIN_USER" -d "$DB" \
    -v ON_ERROR_STOP=1 \
    >/dev/null

echo "enricher_ro: LOGIN enabled (host=$HOST db=$DB)"
