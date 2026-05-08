-- 019_scheduler_entries: 크롤러 진입 URL 의 source-of-truth 테이블 신설 (이슈 #328)
--
-- 배경:
--   기존 internal/scheduler/entries.go 의 sourceCategoryURLs map 이 hardcoded — 운영 중
--   추가 / 변경 시 재배포 필요. 또한 단일 interval (2h) 만 지원하여 카테고리 별 차별화된
--   빈도 (실시간 community vs long-tail search) 부재.
--
-- 본 테이블은 3 카테고리 (news / community / search) 의 진입 URL + per-entry interval 을
-- DB 에 저장. SchedulerEntryResolver (5분 TTL cache) 가 운영 중 변경을 자연 반영.
--
-- 스키마:
--   - category: 'news' | 'community' | 'search' (CHECK 제약, 신규 카테고리 시 application
--     검증으로 cover — DB ENUM 회피하여 확장 친화)
--   - source_name: fetcher_rules.source_name 과 일치 (조인 가능). search 카테고리는 'google' 등
--   - url: 진입 URL. search 의 경우 base URL 또는 sentinel ("search:google" 등)
--   - target_type: 'category' (news/community) | 'search_results' (search) — 향후 확장 가능
--   - interval_seconds: per-entry interval (INTEGER for Go time.Duration 단순 변환)
--   - priority: 1=high / 2=normal / 3=low — fetcher worker pool 라우팅에 사용
--   - enabled: 운영자 manual 토글
--   - metadata: JSONB — 카테고리별 확장 슬롯 (search 의 cse_id / language / region 등)
--   - notes: 운영 가시성용 free-form
--
-- Seed:
--   기존 internal/scheduler/entries.go 의 4 source / 29 URL 을 category='news' 로 이전.
--   interval_seconds=7200 (2h) — 기존 동작 보존. Sub A (이슈 #329) 에서 30m 로 단축.

CREATE TABLE IF NOT EXISTS scheduler_entries (
  id               BIGSERIAL PRIMARY KEY,
  category         TEXT NOT NULL CHECK (category IN ('news', 'community', 'search')),
  source_name      TEXT NOT NULL,
  url              TEXT NOT NULL,
  target_type      TEXT NOT NULL DEFAULT 'category',
  interval_seconds INTEGER NOT NULL CHECK (interval_seconds > 0),
  priority         INTEGER NOT NULL DEFAULT 2 CHECK (priority BETWEEN 1 AND 3),
  enabled          BOOLEAN NOT NULL DEFAULT TRUE,
  metadata         JSONB NOT NULL DEFAULT '{}',
  notes            TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT scheduler_entries_unique UNIQUE (category, source_name, url)
);

CREATE INDEX IF NOT EXISTS idx_scheduler_entries_enabled
  ON scheduler_entries (category, enabled)
  WHERE enabled = TRUE;

COMMENT ON TABLE scheduler_entries IS
  '크롤러 진입 URL 의 DB 기반 source-of-truth (이슈 #328). 기존 hardcoded sourceCategoryURLs 대체.';

COMMENT ON COLUMN scheduler_entries.metadata IS
  '카테고리별 확장 슬롯. search 의 cse_id / language / region, community 의 sub-board 등.';

-- Seed: 29 news URLs (기존 sourceCategoryURLs map 에서 이전).
--   naver: 6 sections (politics/economy/society/culture/world/it)
--   daum: 9 sections
--   yonhap: 5 sections
--   cnn: 9 sections

INSERT INTO scheduler_entries (category, source_name, url, target_type, interval_seconds, priority, enabled, notes)
VALUES
  -- naver (6)
  ('news', 'naver', 'https://news.naver.com/section/100', 'category', 7200, 2, TRUE, 'politics'),
  ('news', 'naver', 'https://news.naver.com/section/101', 'category', 7200, 2, TRUE, 'economy'),
  ('news', 'naver', 'https://news.naver.com/section/102', 'category', 7200, 2, TRUE, 'society'),
  ('news', 'naver', 'https://news.naver.com/section/103', 'category', 7200, 2, TRUE, 'culture'),
  ('news', 'naver', 'https://news.naver.com/section/104', 'category', 7200, 2, TRUE, 'world'),
  ('news', 'naver', 'https://news.naver.com/section/105', 'category', 7200, 2, TRUE, 'it'),
  -- daum (9)
  ('news', 'daum', 'https://news.daum.net/politics', 'category', 7200, 2, TRUE, 'politics'),
  ('news', 'daum', 'https://news.daum.net/economic', 'category', 7200, 2, TRUE, 'economy'),
  ('news', 'daum', 'https://news.daum.net/society', 'category', 7200, 2, TRUE, 'society'),
  ('news', 'daum', 'https://news.daum.net/culture', 'category', 7200, 2, TRUE, 'culture'),
  ('news', 'daum', 'https://news.daum.net/foreign', 'category', 7200, 2, TRUE, 'world'),
  ('news', 'daum', 'https://news.daum.net/tech', 'category', 7200, 2, TRUE, 'it'),
  ('news', 'daum', 'https://news.daum.net/climate', 'category', 7200, 2, TRUE, 'climate'),
  ('news', 'daum', 'https://news.daum.net/life', 'category', 7200, 2, TRUE, 'life'),
  ('news', 'daum', 'https://news.daum.net/understanding', 'category', 7200, 2, TRUE, 'column'),
  -- yonhap (5)
  ('news', 'yonhap', 'https://www.yna.co.kr/politics/all', 'category', 7200, 2, TRUE, 'politics'),
  ('news', 'yonhap', 'https://www.yna.co.kr/economy/all', 'category', 7200, 2, TRUE, 'economy'),
  ('news', 'yonhap', 'https://www.yna.co.kr/society/all', 'category', 7200, 2, TRUE, 'society'),
  ('news', 'yonhap', 'https://www.yna.co.kr/culture/all', 'category', 7200, 2, TRUE, 'culture'),
  ('news', 'yonhap', 'https://www.yna.co.kr/international/all', 'category', 7200, 2, TRUE, 'world'),
  -- cnn (9)
  ('news', 'cnn', 'https://edition.cnn.com', 'category', 7200, 2, TRUE, 'top'),
  ('news', 'cnn', 'https://edition.cnn.com/us', 'category', 7200, 2, TRUE, 'us'),
  ('news', 'cnn', 'https://edition.cnn.com/world', 'category', 7200, 2, TRUE, 'world'),
  ('news', 'cnn', 'https://edition.cnn.com/politics', 'category', 7200, 2, TRUE, 'politics'),
  ('news', 'cnn', 'https://edition.cnn.com/business', 'category', 7200, 2, TRUE, 'business'),
  ('news', 'cnn', 'https://edition.cnn.com/business/tech', 'category', 7200, 2, TRUE, 'tech'),
  ('news', 'cnn', 'https://edition.cnn.com/health', 'category', 7200, 2, TRUE, 'health'),
  ('news', 'cnn', 'https://edition.cnn.com/entertainment', 'category', 7200, 2, TRUE, 'entertainment'),
  ('news', 'cnn', 'https://edition.cnn.com/sport', 'category', 7200, 2, TRUE, 'sports')
ON CONFLICT (category, source_name, url) DO NOTHING;
