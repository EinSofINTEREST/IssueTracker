-- 031_enricher_ro_role: enrich agent 가 MCP postgres 로 직접 사용할 read-only role (이슈 #472)
--
-- 배경:
--   enricher worker 가 in-Go tokenize / tokenOverlap 으로 후보 article 을 ranking 하던 방식은
--   ASCII 영문만 토큰으로 인정하여 한글 title 에서 모든 row 의 score=0 발생 (이슈 #469).
--   본 migration 으로 LLM agent 가 read-only 계정으로 contents / enriched_contents 를 직접
--   조회할 수 있게 만들어, 후보 선정 전략을 prompt 측면에서 유연하게 변경 가능하게 함.
--
-- 보안 모델:
--   - 컬럼 단위 GRANT — contents.body / content_bodies 처럼 토큰 비용이 큰 본문 컬럼 노출 회피
--   - SELECT 만 부여 — 쓰기 / DDL / 트랜잭션 일체 차단
--   - statement_timeout 5s — runaway query 보호
--   - idle_in_transaction_session_timeout 10s — 누수된 트랜잭션 차단
--
-- 운영:
--   - 본 migration 은 NOLOGIN 으로만 생성 — 자격증명을 git 에 commit 하지 않기 위함 (PR #473
--     coderabbit major 지적). 모든 환경 (dev / staging / prod) 에서 배포 직후 별도 secret
--     관리 단계로 LOGIN + PASSWORD 활성화 필요:
--       psql -c "ALTER ROLE enricher_ro WITH LOGIN PASSWORD '<runtime-secret>';"
--   - dev/CI 편의를 위해 scripts/dev/enable-enricher-ro.sh 가 .env 값으로 1회 실행 가능.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'enricher_ro') THEN
    CREATE ROLE enricher_ro NOLOGIN;
  END IF;
END$$;

-- 안전 가드: 의도치 않게 다른 권한이 부여되어 있을 경우 일괄 회수.
REVOKE ALL ON ALL TABLES    IN SCHEMA public FROM enricher_ro;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM enricher_ro;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM enricher_ro;

-- runaway 쿼리 / 누수 트랜잭션 차단.
ALTER ROLE enricher_ro SET statement_timeout                    = '5s';
ALTER ROLE enricher_ro SET idle_in_transaction_session_timeout  = '10s';
ALTER ROLE enricher_ro SET lock_timeout                         = '2s';

-- 스키마 접근 (USAGE) 만 부여 — 객체 단위 GRANT 와 별개로 필수.
GRANT USAGE ON SCHEMA public TO enricher_ro;

-- contents: 메타데이터 컬럼만 노출. body 는 content_bodies 에 있고 본 role 에 미부여.
-- 컬럼 단위 GRANT 는 PostgreSQL 9.x+ 지원.
GRANT SELECT (
  id, source_id, source_type, country, language,
  title, author, published_at, updated_at, category, tags,
  url, canonical_url, content_hash, reliability, created_at
) ON contents TO enricher_ro;

-- enriched_contents: 전부 노출 가능 — 본문/HTML 같은 무거운 컬럼이 없음.
-- factors 등 JSONB 는 신뢰도 비교/heuristic 입력에 유용.
GRANT SELECT ON enriched_contents TO enricher_ro;

COMMENT ON ROLE enricher_ro IS
  'enrich agent 가 MCP postgres 로 직접 사용하는 read-only 계정 (이슈 #472). '
  'contents 의 메타 컬럼 + enriched_contents 전체에 SELECT 만 허용.';
