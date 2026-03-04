-- 004_schema_improvements: 스키마 개선 — 참조 관계 명확화, 인덱스 보완, Blob 오프로딩 준비
--
-- 변경 사항:
--   1. raw_contents ↔ news_articles 참조 관계 추가 (raw_content_id FK)
--   2. news_articles 누락 인덱스 추가 (language, tags, category)
--   3. contents 누락 인덱스 추가 (source_id)
--   4. raw_contents에 blob_key 컬럼 추가 (Kafka Blob / S3 오프로딩 준비)

-- ─────────────────────────────────────────────────────────────────────────────
-- 1. raw_contents ↔ news_articles 참조 관계
-- ─────────────────────────────────────────────────────────────────────────────

-- 크롤링 파이프라인: 크롤러 → raw_contents → news_articles → contents
-- news_articles에서 원본 raw_content를 역추적할 수 있도록 FK 추가
ALTER TABLE news_articles
  ADD COLUMN raw_content_id VARCHAR(255),
  ADD CONSTRAINT news_articles_raw_content_fk
    FOREIGN KEY (raw_content_id) REFERENCES raw_contents(id)
    ON DELETE SET NULL;

-- ─────────────────────────────────────────────────────────────────────────────
-- 2. news_articles 누락 인덱스
-- ─────────────────────────────────────────────────────────────────────────────

-- 언어별 조회 (NewsArticleFilter.Language 확장 대비)
CREATE INDEX IF NOT EXISTS idx_news_articles_language
  ON news_articles (language, published_at DESC);

-- GIN 인덱스: tags 배열 포함 검색 (@> 연산자, contents와 동일한 전략)
CREATE INDEX IF NOT EXISTS idx_news_articles_tags
  ON news_articles USING GIN(tags);

-- 카테고리 필터 (NULL 제외 — 파셜 인덱스)
CREATE INDEX IF NOT EXISTS idx_news_articles_category
  ON news_articles (category, country, published_at DESC)
  WHERE category IS NOT NULL;

-- raw_content_id FK 조회 성능 (NULL 제외 — 파셜 인덱스)
CREATE INDEX IF NOT EXISTS idx_news_articles_raw_content_id
  ON news_articles (raw_content_id)
  WHERE raw_content_id IS NOT NULL;

-- ─────────────────────────────────────────────────────────────────────────────
-- 3. contents 누락 인덱스
-- ─────────────────────────────────────────────────────────────────────────────

-- source_id 기반 조회 (ContentFilter.Source)
-- 빈 source_id('') 제외 — 파셜 인덱스로 인덱스 크기 절감
CREATE INDEX IF NOT EXISTS idx_contents_source_id
  ON contents (source_id)
  WHERE source_id != '';

-- ─────────────────────────────────────────────────────────────────────────────
-- 4. raw_contents Blob 오프로딩 준비
-- ─────────────────────────────────────────────────────────────────────────────

-- blob_key: S3/오브젝트 스토리지 참조 키 (Kafka Blob 오프로딩 전환 시 활용)
-- NULL: html 컬럼에 직접 저장 중 / NOT NULL: 오브젝트 스토리지로 오프로딩 완료
ALTER TABLE raw_contents
  ADD COLUMN blob_key TEXT;

CREATE INDEX IF NOT EXISTS idx_raw_contents_blob_key
  ON raw_contents (blob_key)
  WHERE blob_key IS NOT NULL;
