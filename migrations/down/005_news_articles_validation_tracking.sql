-- 005_news_articles_validation_tracking 롤백 — 역순으로 모든 변경 사항 제거

DROP INDEX IF EXISTS idx_news_articles_validation_status;

ALTER TABLE news_articles
  DROP CONSTRAINT IF EXISTS news_articles_validation_status_check,
  DROP COLUMN IF EXISTS reject_detail,
  DROP COLUMN IF EXISTS reject_code,
  DROP COLUMN IF EXISTS validation_status;
