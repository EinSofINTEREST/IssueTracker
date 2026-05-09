-- 021_community_sources_seed: Community 카테고리 진입점 등록 (이슈 #330, #328 sub B)
--
-- 배경:
--   기존 4 source 는 모두 source_type='news' 로 뉴스 보도 위주. 이슈 #328 의 3 카테고리
--   설계에 따라 실시간 여론 파악을 위한 community 진입점을 별도 등록 — interval 10m,
--   anti-bot 대응 chromedp 우선.
--
-- 등록 대상 (1차 6개 사이트):
--   국내 4: theqoo / clien / fmkorea / dcinside (주요 한국 인기 커뮤니티)
--   해외 2: reddit / hackernews (국제 community + tech 커뮤니티)
--
-- 정책:
--   - fetcher='chromedp' — 대부분 SPA / anti-bot 으로 raw HTTP 의존 회피 (HN 만 정적 HTML 이라
--     goquery 가능하지만 일관성 위해 chromedp 통일)
--   - requests_per_hour 보수 (100~200) — IP 단위 token bucket 으로 throttle (#323)
--   - parsing_rules 는 LLM 자동 학습 (#326 claudegen multi-step) 에 위임 — 본 마이그레이션은
--     fetcher_rules + scheduler_entries 만 seed
--   - scheduler_entries.interval_seconds = 600 (10m) — 실시간 여론 빈도

-- ============================================================================
-- Step 1: fetcher_rules (host_pattern 단위 source 등록)
-- ============================================================================

INSERT INTO fetcher_rules (host_pattern, fetcher, reason, source_name, source_type, country, language, base_url, requests_per_hour)
VALUES
  -- TheQoo (KR community — 실시간 인기글)
  ('theqoo.net', 'chromedp', 'initial seed from migration 021', 'theqoo', 'community', 'KR', 'ko',
    'https://theqoo.net', 100),

  -- Clien (KR community — 모두의공원 / 새로운 소식)
  ('www.clien.net', 'chromedp', 'initial seed from migration 021', 'clien', 'community', 'KR', 'ko',
    'https://www.clien.net', 100),

  -- FMKorea (KR community — 포텐 / 정치 베스트)
  ('www.fmkorea.com', 'chromedp', 'initial seed from migration 021', 'fmkorea', 'community', 'KR', 'ko',
    'https://www.fmkorea.com', 100),

  -- DCInside (KR community — 갤러리)
  ('gallery.dcinside.com', 'chromedp', 'initial seed from migration 021', 'dcinside', 'community', 'KR', 'ko',
    'https://gallery.dcinside.com', 100),

  -- Reddit (국제 community)
  ('www.reddit.com', 'chromedp', 'initial seed from migration 021', 'reddit', 'community', 'US', 'en',
    'https://www.reddit.com', 200),
  ('old.reddit.com', 'chromedp', 'initial seed from migration 021 — old reddit fallback', 'reddit', 'community', 'US', 'en',
    'https://www.reddit.com', 200),

  -- Hacker News (국제 community)
  ('news.ycombinator.com', 'chromedp', 'initial seed from migration 021', 'hackernews', 'community', 'US', 'en',
    'https://news.ycombinator.com', 200)
ON CONFLICT (host_pattern) DO NOTHING;

-- ============================================================================
-- Step 2: scheduler_entries (진입 URL — interval 10m)
-- ============================================================================
--   각 사이트의 메인 인기 / 정치 / 뉴스 카테고리 페이지를 1~3개씩 등록.

INSERT INTO scheduler_entries (category, source_name, url, target_type, interval_seconds, priority, enabled, notes)
VALUES
  -- theqoo (1)
  ('community', 'theqoo', 'https://theqoo.net/hot', 'category', 600, 2, TRUE, 'realtime hot'),

  -- clien (1)
  ('community', 'clien', 'https://www.clien.net/service/board/park', 'category', 600, 2, TRUE, '모두의공원'),

  -- fmkorea (2)
  ('community', 'fmkorea', 'https://www.fmkorea.com/best', 'category', 600, 2, TRUE, 'best'),
  ('community', 'fmkorea', 'https://www.fmkorea.com/index.php?mid=politics', 'category', 600, 2, TRUE, 'politics'),

  -- dcinside (2)
  ('community', 'dcinside', 'https://gallery.dcinside.com/board/lists/?id=politics', 'category', 600, 2, TRUE, 'politics gallery'),
  ('community', 'dcinside', 'https://gallery.dcinside.com/board/lists/?id=baseball_new11', 'category', 600, 2, TRUE, 'baseball gallery'),

  -- reddit (3)
  ('community', 'reddit', 'https://www.reddit.com/r/news', 'category', 600, 2, TRUE, 'r/news'),
  ('community', 'reddit', 'https://www.reddit.com/r/worldnews', 'category', 600, 2, TRUE, 'r/worldnews'),
  ('community', 'reddit', 'https://www.reddit.com/r/politics', 'category', 600, 2, TRUE, 'r/politics'),

  -- hackernews (2)
  ('community', 'hackernews', 'https://news.ycombinator.com/news', 'category', 600, 2, TRUE, 'top'),
  ('community', 'hackernews', 'https://news.ycombinator.com/best', 'category', 600, 2, TRUE, 'best')
ON CONFLICT (category, source_name, url) DO NOTHING;
