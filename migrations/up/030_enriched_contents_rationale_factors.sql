-- 030_enriched_contents_rationale_factors: scorer 진단 정보 영속화 (이슈 #457)
--
-- 배경:
--   PR #455 (이슈 #450) 가 scorer trust_score 만 enriched_contents 에 영속화하고
--   rationale (점수 근거 텍스트) + factors (claim_support_ratio / source_diversity /
--   context_completeness JSONB) 는 ProcessingMessage.Metadata 에만 임시 첨부.
--   본 migration 으로 두 필드를 DB 컬럼으로 영속화 — 운영 진단 / 신뢰도 이상치 회고 /
--   점수 회귀 분석에 활용 가능.
--
-- 운영 영향:
--   ALTER TABLE ADD COLUMN ... DEFAULT 는 PostgreSQL 11+ 에서 metadata-only — full table
--   rewrite 없이 빠르게 적용. 기존 row 는 default value (rationale='' / factors='{}') 를
--   가지게 됨.

ALTER TABLE enriched_contents
  ADD COLUMN rationale TEXT NOT NULL DEFAULT '',
  ADD COLUMN factors   JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN enriched_contents.rationale IS
  'Scorer 가 산출한 trust_score 근거 텍스트 (운영 진단용, 이슈 #457).';
COMMENT ON COLUMN enriched_contents.factors IS
  '{claim_support_ratio, source_diversity, context_completeness} JSONB — 점수 구성 진단 필드 (이슈 #457).';
