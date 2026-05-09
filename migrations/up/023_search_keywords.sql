-- 023_search_keywords: 검색 기반 진입을 위한 keyword set 테이블 신설 (이슈 #336 / 부모 #331)
--
-- 배경:
--   #328 의 sub-issue C (#331) — Google CSE 기반 검색 진입점. 운영자 정의 keyword set 을
--   동적으로 관리 + DB 변경 즉시 반영 (다음 search cycle 부터). 본 마이그레이션은 phase 1 —
--   테이블 + 초기 seed. CSE client / handler / scheduler 통합은 phase 2 (#337).
--
-- 스키마:
--   - keyword: UNIQUE — 동일 keyword 중복 등록 차단
--   - enabled: 운영자 manual 토글
--   - source: 'manual' (운영자 인입) | 'auto' (entity / 트렌드 자동 추출 — 후속 이슈)
--   - language / region: Google CSE 의 lr / gl 파라미터. '' = 미지정 (전체)
--   - last_searched_at: 마지막 검색 cycle 시각 — round-robin / 우선순위 알고리즘에 활용 가능
--
-- Seed:
--   초기 manual keyword 16개 — KR/EN 균형 + 일반 시사 / 정치 / 경제 / 기술 cover.
--   본 seed 는 운영자가 운영 중 INSERT/UPDATE 로 자유롭게 조정 가능 (DB-driven).

CREATE TABLE IF NOT EXISTS search_keywords (
  id               BIGSERIAL PRIMARY KEY,
  keyword          TEXT NOT NULL UNIQUE,
  enabled          BOOLEAN NOT NULL DEFAULT TRUE,
  source           TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'auto')),
  language         VARCHAR(10) NOT NULL DEFAULT '',
  region           VARCHAR(10) NOT NULL DEFAULT '',
  notes            TEXT NOT NULL DEFAULT '',
  last_searched_at TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_search_keywords_enabled
  ON search_keywords (enabled, language, region)
  WHERE enabled = TRUE;

COMMENT ON TABLE search_keywords IS
  'Google CSE 기반 search exploration 의 keyword set (이슈 #331). 운영자 manual + 후속 auto 인입.';

COMMENT ON COLUMN search_keywords.source IS
  '''manual'' = 운영자 직접 등록 / ''auto'' = entity·trend 자동 추출 (#331 후속 이슈)';

COMMENT ON COLUMN search_keywords.language IS
  'Google CSE lr 파라미터 (''ko'' / ''en'' / ''''). 빈 문자열 = 미지정';

COMMENT ON COLUMN search_keywords.region IS
  'Google CSE gl 파라미터 (''kr'' / ''us'' / ''''). 빈 문자열 = 미지정';

-- ============================================================================
-- Seed: 초기 manual keyword 16개 (KR 8 + EN 8)
-- ============================================================================

INSERT INTO search_keywords (keyword, source, language, region, notes)
VALUES
  -- KR 8
  ('대선 여론조사',    'manual', 'ko', 'kr', '정치 — 선거 여론'),
  ('금리 인상',        'manual', 'ko', 'kr', '경제 — 통화정책'),
  ('부동산 정책',      'manual', 'ko', 'kr', '경제·사회'),
  ('AI 규제',          'manual', 'ko', 'kr', '기술·정책'),
  ('반도체 수출',      'manual', 'ko', 'kr', '경제·산업'),
  ('전기차 보조금',    'manual', 'ko', 'kr', '경제·환경'),
  ('기후 변화',        'manual', 'ko', 'kr', '환경'),
  ('의료 개혁',        'manual', 'ko', 'kr', '사회·정책'),

  -- EN 8
  ('US election',          'manual', 'en', 'us', 'politics'),
  ('Federal Reserve rate', 'manual', 'en', 'us', 'economy — monetary'),
  ('AI regulation',        'manual', 'en', 'us', 'tech policy'),
  ('semiconductor export', 'manual', 'en', 'us', 'economy — industry'),
  ('climate policy',       'manual', 'en', 'us', 'environment'),
  ('healthcare reform',    'manual', 'en', 'us', 'social policy'),
  ('cybersecurity breach', 'manual', 'en', 'us', 'tech security'),
  ('inflation report',     'manual', 'en', 'us', 'economy — prices')
ON CONFLICT (keyword) DO NOTHING;
