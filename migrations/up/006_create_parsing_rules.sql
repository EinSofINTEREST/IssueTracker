-- 006_create_parsing_rules: 사이트별 파싱 규칙을 DB로 일원화 (이슈 #100)
--
-- 배경:
--   기존에는 naver/daum/yonhap/cnn 각각에 parser.go 가 있고 selector 가 코드에
--   hardcode 되어 있어 새 사이트 지원 시 코드 추가 + 재배포가 필요했다. 본 테이블은
--   사이트별 파싱 규칙을 단일 source 로 관리하여 단일 rule-based parser engine 이
--   런타임에 규칙을 조회해 동작하도록 한다.
--
-- 스키마:
--   - source_name + host_pattern + target_type + version 의 자연키
--   - selectors: JSONB — 필드별 CSS selector / attribute / multi 등 선택값 보관
--     (top-level 컬럼으로 빼면 새 필드 추가 시 migration 필요 → JSONB 가 진화 친화적)
--   - enabled: 동일 (source_name, target_type) 안에서 어떤 version 이 활성인지 표시
--   - 활성 규칙 1건 보장은 application 레벨 책임 (DB unique 로 강제하지 않음 —
--     운영자가 새 version 을 enabled=true 로 바꾼 직후 잠시 두 row 활성 가능)

CREATE TABLE IF NOT EXISTS parsing_rules (
  id           BIGSERIAL    PRIMARY KEY,

  -- 자연키 — application 에서 (source_name, host_pattern, target_type, version) 4-tuple 로 lookup
  source_name  VARCHAR(100) NOT NULL,    -- "naver" / "cnn" / "yonhap" / "blog.example.com" 등
  host_pattern VARCHAR(255) NOT NULL,    -- "n.news.naver.com" / "edition.cnn.com" — URL host 매칭
  target_type  VARCHAR(20)  NOT NULL,    -- "page" (단일 컨텐츠) / "list" (링크-허브)
  version      INT          NOT NULL DEFAULT 1,

  -- 활성화 플래그
  enabled      BOOLEAN      NOT NULL DEFAULT TRUE,

  -- 필드별 CSS selector / attribute / multi 등 — JSONB 로 유연성 확보
  selectors    JSONB        NOT NULL DEFAULT '{}'::jsonb,

  -- 메타데이터
  description  TEXT,                     -- 운영자 메모 (LLM 생성 / 휴먼 review 결과 등)
  created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
  updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

  CONSTRAINT parsing_rules_target_type_check
    CHECK (target_type IN ('page', 'list')),
  CONSTRAINT parsing_rules_version_positive
    CHECK (version > 0),
  CONSTRAINT parsing_rules_natural_key_unique
    UNIQUE (source_name, host_pattern, target_type, version)
);

-- URL host 기반 lookup — host_pattern + target_type + enabled 가 핫패스
CREATE INDEX IF NOT EXISTS idx_parsing_rules_lookup
  ON parsing_rules (host_pattern, target_type, enabled)
  WHERE enabled = TRUE;

-- 운영 대시보드 — source 별 활성 rule 조회
CREATE INDEX IF NOT EXISTS idx_parsing_rules_source_enabled
  ON parsing_rules (source_name, enabled, target_type);

-- updated_at auto-touch trigger
CREATE OR REPLACE FUNCTION parsing_rules_touch_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS parsing_rules_touch_updated_at ON parsing_rules;
CREATE TRIGGER parsing_rules_touch_updated_at
  BEFORE UPDATE ON parsing_rules
  FOR EACH ROW EXECUTE FUNCTION parsing_rules_touch_updated_at();
