-- articles 테이블: 파싱된 기사 데이터 저장
-- core.Article 구조체를 직접 매핑합니다.
-- tags, image_urls는 pgx/v5 네이티브 TEXT[] 배열로 저장합니다.

CREATE TABLE IF NOT EXISTS articles (
  id            VARCHAR(255)  NOT NULL,
  source_id     VARCHAR(255)  NOT NULL DEFAULT '',
  country       VARCHAR(2)    NOT NULL,
  language      VARCHAR(10)   NOT NULL,

  -- Content
  title         TEXT          NOT NULL,
  body          TEXT          NOT NULL DEFAULT '',
  summary       TEXT          NOT NULL DEFAULT '',

  -- Metadata
  author        VARCHAR(500)  NOT NULL DEFAULT '',
  published_at  TIMESTAMPTZ   NOT NULL,
  updated_at    TIMESTAMPTZ,
  category      VARCHAR(255)  NOT NULL DEFAULT '',
  tags          TEXT[]        NOT NULL DEFAULT '{}',

  -- Technical
  url           TEXT          NOT NULL,
  canonical_url TEXT          NOT NULL DEFAULT '',
  image_urls    TEXT[]        NOT NULL DEFAULT '{}',

  -- Quality
  content_hash  VARCHAR(64)   NOT NULL DEFAULT '',
  word_count    INTEGER       NOT NULL DEFAULT 0,

  created_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW(),

  CONSTRAINT articles_pkey PRIMARY KEY (id)
);

-- URL 유일성: 동일 URL의 중복 저장 방지 (upsert 기준)
CREATE UNIQUE INDEX IF NOT EXISTS idx_articles_url
  ON articles(url);

-- content_hash 기반 중복 감지 (GetByContentHash)
-- 빈 hash('') 제외 — 파셜 인덱스로 인덱스 크기 절감
CREATE INDEX IF NOT EXISTS idx_articles_content_hash
  ON articles(content_hash)
  WHERE content_hash != '';

-- 국가별 최신순 조회 (ListByCountry)
CREATE INDEX IF NOT EXISTS idx_articles_country_published
  ON articles(country, published_at DESC);

-- 언어별 조회
CREATE INDEX IF NOT EXISTS idx_articles_language
  ON articles(language, published_at DESC);

-- 카테고리 필터 (ArticleFilter.Category)
CREATE INDEX IF NOT EXISTS idx_articles_category
  ON articles(category, country, published_at DESC)
  WHERE category != '';

-- GIN 인덱스: tags 배열 포함 검색 (@> 연산자)
CREATE INDEX IF NOT EXISTS idx_articles_tags
  ON articles USING GIN(tags);
