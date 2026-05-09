-- 022_community_sources_more: Community 진입점 추가 등록 — 8 사이트 (이슈 #330 후속)
--
-- 배경:
--   migration 021 의 6 사이트 (theqoo / clien / fmkorea / dcinside / reddit / hackernews)
--   이후 사용자 요청으로 더 다양한 커뮤니티 cover. 본 마이그레이션은 국내 5 + 해외 3 추가.
--
-- 신규 대상 (8 사이트):
--   국내 5: ruliweb / mlbpark / inven / bobaedream / pgr21 (gaming / sports / general)
--   해외 3: slashdot / lemmy.world / tildes (tech / decentralized / curated)
--
-- 정책 (021 과 동일):
--   - fetcher='chromedp' 통일 — anti-bot 대응
--   - requests_per_hour 보수 (KR 100 / international 200)
--   - parsing_rules 는 LLM 자동 학습 (#326) 에 위임
--   - scheduler_entries.interval_seconds = 600 (10m)

-- ============================================================================
-- Step 1: fetcher_rules
-- ============================================================================

INSERT INTO fetcher_rules (host_pattern, fetcher, reason, source_name, source_type, country, language, base_url, requests_per_hour)
VALUES
  -- Ruliweb (KR — 유머 / 베스트 게시판)
  ('bbs.ruliweb.com', 'chromedp', 'initial seed from migration 022', 'ruliweb', 'community', 'KR', 'ko',
    'https://bbs.ruliweb.com', 100),

  -- MLBPark (KR — 불펜 / 베스트 / 야구)
  ('mlbpark.donga.com', 'chromedp', 'initial seed from migration 022', 'mlbpark', 'community', 'KR', 'ko',
    'https://mlbpark.donga.com', 100),

  -- Inven (KR — 게임 커뮤니티)
  ('www.inven.co.kr', 'chromedp', 'initial seed from migration 022', 'inven', 'community', 'KR', 'ko',
    'https://www.inven.co.kr', 100),

  -- Bobaedream (KR — 자동차 / 베스트)
  ('www.bobaedream.co.kr', 'chromedp', 'initial seed from migration 022', 'bobaedream', 'community', 'KR', 'ko',
    'https://www.bobaedream.co.kr', 100),

  -- PGR21 (KR — e스포츠 / 자유게시판)
  ('www.pgr21.com', 'chromedp', 'initial seed from migration 022', 'pgr21', 'community', 'KR', 'ko',
    'https://www.pgr21.com', 100),

  -- Slashdot (US — tech news community)
  ('slashdot.org', 'chromedp', 'initial seed from migration 022', 'slashdot', 'community', 'US', 'en',
    'https://slashdot.org', 200),
  ('news.slashdot.org', 'chromedp', 'initial seed from migration 022', 'slashdot', 'community', 'US', 'en',
    'https://slashdot.org', 200),

  -- Lemmy.world (decentralized reddit alternative)
  ('lemmy.world', 'chromedp', 'initial seed from migration 022', 'lemmy', 'community', 'US', 'en',
    'https://lemmy.world', 200),

  -- Tildes (curated discussion)
  ('tildes.net', 'chromedp', 'initial seed from migration 022', 'tildes', 'community', 'US', 'en',
    'https://tildes.net', 200)
ON CONFLICT (host_pattern) DO NOTHING;

-- ============================================================================
-- Step 2: scheduler_entries — interval 10m
-- ============================================================================

INSERT INTO scheduler_entries (category, source_name, url, target_type, interval_seconds, priority, enabled, notes)
VALUES
  -- ruliweb (2)
  ('community', 'ruliweb',    'https://bbs.ruliweb.com/best/humor',                                 'category', 600, 2, TRUE, 'humor best'),
  ('community', 'ruliweb',    'https://bbs.ruliweb.com/community',                                  'category', 600, 2, TRUE, 'community'),

  -- mlbpark (2)
  ('community', 'mlbpark',    'https://mlbpark.donga.com/mp/?b=bullpen',                            'category', 600, 2, TRUE, 'bullpen'),
  ('community', 'mlbpark',    'https://mlbpark.donga.com/mp/?b=political',                          'category', 600, 2, TRUE, 'political'),

  -- inven (2)
  ('community', 'inven',      'https://www.inven.co.kr/board/it',                                   'category', 600, 2, TRUE, 'IT 게시판'),
  ('community', 'inven',      'https://www.inven.co.kr/board/webzine',                              'category', 600, 2, TRUE, 'webzine'),

  -- bobaedream (1)
  ('community', 'bobaedream', 'https://www.bobaedream.co.kr/list?code=best',                        'category', 600, 2, TRUE, 'best 게시판'),

  -- pgr21 (1)
  ('community', 'pgr21',      'https://www.pgr21.com/freedom',                                      'category', 600, 2, TRUE, '자유게시판'),

  -- slashdot (2)
  ('community', 'slashdot',   'https://slashdot.org/',                                              'category', 600, 2, TRUE, 'main top'),
  ('community', 'slashdot',   'https://news.slashdot.org/',                                         'category', 600, 2, TRUE, 'news'),

  -- lemmy (2)
  ('community', 'lemmy',      'https://lemmy.world/c/news',                                         'category', 600, 2, TRUE, 'c/news'),
  ('community', 'lemmy',      'https://lemmy.world/c/worldnews',                                    'category', 600, 2, TRUE, 'c/worldnews'),

  -- tildes (2)
  ('community', 'tildes',     'https://tildes.net/',                                                'category', 600, 2, TRUE, 'front page'),
  ('community', 'tildes',     'https://tildes.net/~news',                                           'category', 600, 2, TRUE, '~news')
ON CONFLICT (category, source_name, url) DO NOTHING;
