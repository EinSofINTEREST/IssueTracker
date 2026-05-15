-- 030 (down): scorer 진단 컬럼 제거.
-- 데이터 손실 주의 — 운영 환경에서 rollback 시 rationale/factors 영구 손실.

ALTER TABLE enriched_contents
  DROP COLUMN IF EXISTS rationale,
  DROP COLUMN IF EXISTS factors;
