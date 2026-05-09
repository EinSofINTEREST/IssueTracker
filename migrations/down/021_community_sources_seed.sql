-- 021 DOWN: community 진입점 제거 — 본 마이그레이션이 INSERT 한 정확한 row 만 삭제.
--
-- source_name 만으로 매칭하면 운영자 manual 추가한 같은 source_name row 까지 삭제될 위험
-- (CodeRabbit Major 반영). 정확한 (host_pattern) / (category, source_name, url) set 으로 한정.
--
-- parsing_rules 의 LLM 자동 학습 결과 (host 별 row) 는 본 DOWN 에서 건드리지 않음 —
-- 운영자가 별도로 정리.

-- scheduler_entries: 정확한 url set
DELETE FROM scheduler_entries
WHERE category = 'community'
  AND (source_name, url) IN (
    ('theqoo',     'https://theqoo.net/hot'),
    ('clien',      'https://www.clien.net/service/board/park'),
    ('fmkorea',    'https://www.fmkorea.com/best'),
    ('fmkorea',    'https://www.fmkorea.com/index.php?mid=politics'),
    ('dcinside',   'https://gall.dcinside.com/board/lists/?id=politics'),
    ('dcinside',   'https://gall.dcinside.com/board/lists/?id=baseball_new11'),
    ('reddit',     'https://www.reddit.com/r/news'),
    ('reddit',     'https://www.reddit.com/r/worldnews'),
    ('reddit',     'https://www.reddit.com/r/politics'),
    ('hackernews', 'https://news.ycombinator.com/news'),
    ('hackernews', 'https://news.ycombinator.com/best')
  );

-- fetcher_rules: 정확한 host_pattern set
DELETE FROM fetcher_rules
WHERE host_pattern IN (
  'theqoo.net',
  'www.clien.net',
  'www.fmkorea.com',
  'gall.dcinside.com',
  'gallery.dcinside.com',
  'www.reddit.com',
  'old.reddit.com',
  'news.ycombinator.com'
);
