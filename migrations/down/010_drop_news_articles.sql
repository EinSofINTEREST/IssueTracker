-- 010_drop_news_articles down: news_articles 테이블 복원 (이슈 #161 롤백)
--
-- 003 + 005 의 합본 — 원본 schema 를 한 번에 재구성합니다.
-- 본 down 을 적용해도 003 / 005 의 down 을 다시 호출할 필요는 없습니다 (멱등 가드 포함).

CREATE TABLE IF NOT EXISTS news_articles (
  id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),

  source_name  VARCHAR(100) NOT NULL,
  source_type  VARCHAR(50)  NOT NULL,
  country      CHAR(2)      NOT NULL,
  language     VARCHAR(10)  NOT NULL,

  url          TEXT         NOT NULL,
  title        TEXT         NOT NULL,
  body         TEXT         NOT NULL,
  summary      TEXT,
  author       TEXT,

  category     VARCHAR(100),
  tags         TEXT[]       NOT NULL DEFAULT '{}',
  image_urls   TEXT[]       NOT NULL DEFAULT '{}',

  published_at TIMESTAMPTZ,
  fetched_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
  created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

  -- 005 에서 추가됐던 validation tracking 컬럼
  validation_status TEXT NOT NULL DEFAULT 'pending',
  reject_code       VARCHAR(20),
  reject_detail     TEXT,

  CONSTRAINT news_articles_url_unique UNIQUE (url),
  CONSTRAINT news_articles_validation_status_check
    CHECK (validation_status IN ('pending', 'passed', 'rejected'))
);

CREATE INDEX IF NOT EXISTS idx_news_articles_country_published
  ON news_articles (country, published_at DESC);

CREATE INDEX IF NOT EXISTS idx_news_articles_source_name
  ON news_articles (source_name);

CREATE INDEX IF NOT EXISTS idx_news_articles_fetched_at
  ON news_articles (fetched_at DESC);

CREATE INDEX IF NOT EXISTS idx_news_articles_validation_status
  ON news_articles (validation_status, created_at DESC);
