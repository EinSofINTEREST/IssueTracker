-- raw_contents 테이블: 크롤링된 원본 데이터 저장
-- core.RawContent 구조체를 직접 매핑합니다.
-- SourceInfo는 인덱스/필터 효율성을 위해 플랫 컬럼으로 펼칩니다.
-- 보존 기간: 90일 (RawContentService.PurgeOlderThan으로 관리)

CREATE TABLE IF NOT EXISTS raw_contents (
  id              VARCHAR(255)  NOT NULL,
  url             TEXT          NOT NULL,
  html            TEXT          NOT NULL DEFAULT '',
  status_code     INTEGER       NOT NULL DEFAULT 0,
  fetched_at      TIMESTAMPTZ   NOT NULL,

  -- source_info (core.SourceInfo 플랫 매핑)
  source_country  VARCHAR(2)    NOT NULL DEFAULT '',
  source_type     VARCHAR(50)   NOT NULL DEFAULT '',
  source_name     VARCHAR(255)  NOT NULL DEFAULT '',
  source_base_url TEXT          NOT NULL DEFAULT '',
  source_language VARCHAR(10)   NOT NULL DEFAULT '',

  -- map[string]string / map[string]interface{} → JSONB
  headers         JSONB         NOT NULL DEFAULT '{}',
  metadata        JSONB         NOT NULL DEFAULT '{}',

  created_at      TIMESTAMPTZ   NOT NULL DEFAULT NOW(),

  CONSTRAINT raw_contents_pkey PRIMARY KEY (id)
);

-- URL 기반 중복 확인 (ExistsByURL, GetByURL, Save UNIQUE 위반 감지)
CREATE UNIQUE INDEX IF NOT EXISTS idx_raw_contents_url
  ON raw_contents(url);

-- 날짜 범위 필터 및 DeleteBefore 성능
CREATE INDEX IF NOT EXISTS idx_raw_contents_fetched_at
  ON raw_contents(fetched_at DESC);

-- 소스별 조회 (RawContentFilter.Country + SourceName)
CREATE INDEX IF NOT EXISTS idx_raw_contents_source
  ON raw_contents(source_country, source_name, fetched_at DESC);
