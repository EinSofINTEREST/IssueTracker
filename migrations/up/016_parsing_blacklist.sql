-- 016_parsing_blacklist: page-parse 블랙리스트 테이블 (이슈 #295)
--
-- 배경:
--   카테고리 페이지에서 발견되지만 article 단계에서 의미 있는 컨텐츠가 없는 URL (광고 /
--   sponsored / redirect / 비-article 영역) 을 운영자가 명시적으로 제외하기 위한 저장소.
--   매칭된 URL 은 publisher.Publish 직전에 제거 → fetch / parse 자체 발생 X.
--
-- 스키마:
--   - host_pattern : URL host 매칭 (예: "n.news.naver.com")
--   - path_pattern : URL path RE2 regex. "" 이면 host 전체 차단 (catch-all)
--   - reason       : 운영 가시성 (ad / redirect / sponsored / ...)
--   - source       : 'manual' (운영자 등록) | 'auto' (시스템 자동 색출 — 후속 이슈)
--   - enabled      : 토글 — 운영자가 임시 비활성 가능
--
-- 자연키 (host_pattern, path_pattern) UNIQUE — 동일 매칭 룰 중복 방지.
-- parsing_rules 와 의미 분리: rule 부재 ≠ blacklist (rule disable 은 \"학습 안 함\",
-- blacklist 는 \"발행 자체 차단\").

CREATE TABLE IF NOT EXISTS parsing_blacklist (
  id            BIGSERIAL    PRIMARY KEY,
  host_pattern  TEXT         NOT NULL,
  path_pattern  TEXT         NOT NULL DEFAULT '',
  reason        TEXT         NOT NULL DEFAULT '',
  source        TEXT         NOT NULL CHECK (source IN ('manual', 'auto')),
  enabled       BOOLEAN      NOT NULL DEFAULT TRUE,
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
  UNIQUE (host_pattern, path_pattern)
);

-- enabled row 의 host 단위 lookup 가속 — Matcher 의 (host) → 후보 슬라이스 fetch 핫패스.
CREATE INDEX IF NOT EXISTS idx_parsing_blacklist_host_enabled
  ON parsing_blacklist (host_pattern)
  WHERE enabled = TRUE;

COMMENT ON TABLE  parsing_blacklist                 IS 'page-parse 블랙리스트 (이슈 #295) — 매칭 URL 은 article job 발행 단계에서 drop.';
COMMENT ON COLUMN parsing_blacklist.path_pattern    IS 'RE2 regex. 빈 문자열이면 host 전체 차단 (catch-all).';
COMMENT ON COLUMN parsing_blacklist.source          IS 'manual: 운영자 등록 / auto: 시스템 자동 색출 (후속 이슈).';
