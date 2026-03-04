-- news_articles 테이블: 수집된 뉴스 기사를 단일 테이블로 관리
-- 소스 도메인(source_name)과 국가(country)를 속성으로 포함

CREATE TABLE IF NOT EXISTS news_articles (
  id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),

  -- 소스 정보
  source_name  VARCHAR(100) NOT NULL,    -- 크롤러 도메인: naver, yonhap, cnn, ...
  source_type  VARCHAR(50)  NOT NULL,    -- news, community, social
  country      CHAR(2)      NOT NULL,    -- ISO 3166-1 alpha-2: KR, US
  language     VARCHAR(10)  NOT NULL,    -- ISO 639-1: ko, en

  -- 기사 핵심 정보
  url          TEXT         NOT NULL,
  title        TEXT         NOT NULL,
  body         TEXT         NOT NULL,
  summary      TEXT,
  author       TEXT,

  -- 분류
  category     VARCHAR(100),
  tags         TEXT[]       NOT NULL DEFAULT '{}',
  image_urls   TEXT[]       NOT NULL DEFAULT '{}',

  -- 시간
  published_at TIMESTAMPTZ,              -- NULL: 발행 시각 불명
  fetched_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
  created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

  CONSTRAINT news_articles_url_unique UNIQUE (url)
);

-- 조회 최적화 인덱스
CREATE INDEX IF NOT EXISTS idx_news_articles_country_published
  ON news_articles (country, published_at DESC);

CREATE INDEX IF NOT EXISTS idx_news_articles_source_name
  ON news_articles (source_name);

CREATE INDEX IF NOT EXISTS idx_news_articles_fetched_at
  ON news_articles (fetched_at DESC);
