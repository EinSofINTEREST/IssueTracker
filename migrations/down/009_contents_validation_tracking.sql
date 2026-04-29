-- 009_contents_validation_tracking down: contents 의 validation tracking 컬럼 제거
--
-- 주의: 본 migration 적용 시점에 news_articles 가 아직 존재한다면 (010 미적용 상태)
--       reject 메타데이터 손실이 발생한다. 운영 환경에서는 010 down 을 먼저 적용해
--       news_articles 가 복원된 상태에서 본 down 을 실행해야 안전하다.

DROP INDEX IF EXISTS idx_contents_validation_status;

ALTER TABLE contents
  DROP CONSTRAINT IF EXISTS contents_validation_status_check;

ALTER TABLE contents
  DROP COLUMN IF EXISTS validation_status,
  DROP COLUMN IF EXISTS reject_code,
  DROP COLUMN IF EXISTS reject_detail;
