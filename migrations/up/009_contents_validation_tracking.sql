-- 009_contents_validation_tracking: validator 결과 추적용 컬럼을 contents 로 이전 (이슈 #161)
--
-- 배경:
--   이슈 #135 에서 도입한 validation_status / reject_code / reject_detail 컬럼이
--   도메인 특화 테이블 news_articles 에 위치해 있었으나, 도메인 중립화 (이슈 #161) 의
--   일환으로 contents 테이블에 일원화한다.
--
--   본 migration 적용 후 010 에서 news_articles 자체를 drop 한다.
--
-- 변경 사항:
--   1. validation_status — pending / passed / rejected (NOT NULL, default pending)
--   2. reject_code      — VAL_001 ~ VAL_006 등 (Rejected 시에만 NOT NULL 의미)
--   3. reject_detail    — validator errors message (Rejected 시에만 NOT NULL 의미)
--   4. CHECK 제약       — validation_status 의 enum 강제
--   5. 인덱스          — (validation_status, created_at DESC) 운영 metric / 대시보드용

ALTER TABLE contents
  ADD COLUMN IF NOT EXISTS validation_status TEXT NOT NULL DEFAULT 'pending',
  ADD COLUMN IF NOT EXISTS reject_code       VARCHAR(20),
  ADD COLUMN IF NOT EXISTS reject_detail     TEXT;

ALTER TABLE contents
  DROP CONSTRAINT IF EXISTS contents_validation_status_check;

ALTER TABLE contents
  ADD CONSTRAINT contents_validation_status_check
    CHECK (validation_status IN ('pending', 'passed', 'rejected'));

CREATE INDEX IF NOT EXISTS idx_contents_validation_status
  ON contents (validation_status, created_at DESC);
