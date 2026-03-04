-- contents: 핵심 메타데이터 테이블 (목록/검색 핫 경로)
-- core.Content 구조체의 핵심 필드를 저장합니다.
-- 본문은 content_bodies, 확장 메타데이터는 content_meta에 분리 저장됩니다.
CREATE TABLE IF NOT EXISTS contents (
  id            VARCHAR(255)  NOT NULL,
  source_id     VARCHAR(255)  NOT NULL DEFAULT '',
  source_type   TEXT          NOT NULL DEFAULT 'news',

  country       VARCHAR(2)    NOT NULL,
  language      VARCHAR(10)   NOT NULL,

  -- 핵심 표시 정보
  title         TEXT          NOT NULL,
  author        VARCHAR(500)  NOT NULL DEFAULT '',
  published_at  TIMESTAMPTZ   NOT NULL,
  updated_at    TIMESTAMPTZ,
  category      VARCHAR(255)  NOT NULL DEFAULT '',
  tags          TEXT[]        NOT NULL DEFAULT '{}',

  -- 식별자
  url           TEXT          NOT NULL,
  canonical_url TEXT          NOT NULL DEFAULT '',
  content_hash  VARCHAR(64)   NOT NULL DEFAULT '',

  -- 신뢰도 (0.0: 미검증, 1.0: 최고 신뢰)
  reliability   REAL          NOT NULL DEFAULT 0.0,

  created_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW(),

  CONSTRAINT contents_pkey PRIMARY KEY (id)
);

-- content_bodies: 본문 텍스트 (상세 조회 시에만 JOIN)
-- 목록 쿼리에서 불필요한 대용량 텍스트 로딩을 방지합니다.
CREATE TABLE IF NOT EXISTS content_bodies (
  content_id  VARCHAR(255)  NOT NULL,
  body        TEXT          NOT NULL DEFAULT '',
  summary     TEXT          NOT NULL DEFAULT '',
  word_count  INTEGER       NOT NULL DEFAULT 0,

  CONSTRAINT content_bodies_pkey PRIMARY KEY (content_id),
  CONSTRAINT content_bodies_fk   FOREIGN KEY (content_id)
    REFERENCES contents(id) ON DELETE CASCADE
);

-- content_meta: 확장 메타데이터 (파이프라인에서 업데이트)
-- 이미지 URL, JSONB 확장 데이터는 엔드유저 목록 조회에서 불필요합니다.
CREATE TABLE IF NOT EXISTS content_meta (
  content_id  VARCHAR(255)  NOT NULL,
  image_urls  TEXT[]        NOT NULL DEFAULT '{}',
  extra       JSONB         NOT NULL DEFAULT '{}',

  CONSTRAINT content_meta_pkey PRIMARY KEY (content_id),
  CONSTRAINT content_meta_fk   FOREIGN KEY (content_id)
    REFERENCES contents(id) ON DELETE CASCADE
);

-- ─────────────────────────────────────────────────────────────────────────────
-- contents 인덱스
-- ─────────────────────────────────────────────────────────────────────────────

-- URL 유일성: 동일 URL의 중복 저장 방지 (upsert 기준)
CREATE UNIQUE INDEX IF NOT EXISTS idx_contents_url
  ON contents(url);

-- content_hash 기반 중복 감지 (GetByContentHash)
-- 빈 hash('') 제외 — 파셜 인덱스로 인덱스 크기 절감
CREATE INDEX IF NOT EXISTS idx_contents_content_hash
  ON contents(content_hash)
  WHERE content_hash != '';

-- 국가별 최신순 조회 (ListByCountry)
CREATE INDEX IF NOT EXISTS idx_contents_country_pub
  ON contents(country, published_at DESC);

-- 언어별 조회
CREATE INDEX IF NOT EXISTS idx_contents_language
  ON contents(language, published_at DESC);

-- 카테고리 필터 (ContentFilter.Category)
CREATE INDEX IF NOT EXISTS idx_contents_category
  ON contents(category, country, published_at DESC)
  WHERE category != '';

-- source_type 기반 필터 (ContentFilter.SourceType)
CREATE INDEX IF NOT EXISTS idx_contents_source_type
  ON contents(source_type, country, published_at DESC);

-- 신뢰도 기반 필터 (ContentFilter.MinReliability)
-- reliability=0.0(미검증) 제외 — 파셜 인덱스
CREATE INDEX IF NOT EXISTS idx_contents_reliability
  ON contents(reliability DESC)
  WHERE reliability > 0;

-- GIN 인덱스: tags 배열 포함 검색 (@> 연산자)
CREATE INDEX IF NOT EXISTS idx_contents_tags
  ON contents USING GIN(tags);
