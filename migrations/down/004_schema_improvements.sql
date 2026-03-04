-- 004_schema_improvements 롤백 — 역순으로 모든 변경 사항 제거

-- ─────────────────────────────────────────────────────────────────────────────
-- 4. raw_contents blob_key 제거
-- ─────────────────────────────────────────────────────────────────────────────
DROP INDEX IF EXISTS idx_raw_contents_blob_key;
ALTER TABLE raw_contents DROP COLUMN IF EXISTS blob_key;

-- ─────────────────────────────────────────────────────────────────────────────
-- 3. contents 인덱스 제거
-- ─────────────────────────────────────────────────────────────────────────────
DROP INDEX IF EXISTS idx_contents_source_id;

-- ─────────────────────────────────────────────────────────────────────────────
-- 2. news_articles 인덱스 제거
-- ─────────────────────────────────────────────────────────────────────────────
DROP INDEX IF EXISTS idx_news_articles_raw_content_id;
DROP INDEX IF EXISTS idx_news_articles_category;
DROP INDEX IF EXISTS idx_news_articles_tags;
DROP INDEX IF EXISTS idx_news_articles_language;

-- ─────────────────────────────────────────────────────────────────────────────
-- 1. news_articles raw_content_id FK 및 컬럼 제거
-- ─────────────────────────────────────────────────────────────────────────────
ALTER TABLE news_articles DROP CONSTRAINT IF EXISTS news_articles_raw_content_fk;
ALTER TABLE news_articles DROP COLUMN IF EXISTS raw_content_id;
