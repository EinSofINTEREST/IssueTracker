-- 022 DOWN: community 추가 진입점 (8 사이트) 제거 — 본 마이그레이션이 INSERT 한 정확한 row 만 삭제.
--
-- (host_pattern) / (category, source_name, url) set 으로 한정 — source_name 만으로 매칭하면
-- 운영자 manual 추가한 같은 source_name row 까지 삭제될 위험 (021 의 CodeRabbit Major 패턴 반영).
--
-- parsing_rules 의 LLM 자동 학습 결과 (host 별 row) 는 본 DOWN 에서 건드리지 않음 —
-- 운영자가 별도로 정리.

-- scheduler_entries: 정확한 url set
DELETE FROM scheduler_entries
WHERE category = 'community'
  AND (source_name, url) IN (
    ('ruliweb',    'https://bbs.ruliweb.com/best/humor'),
    ('ruliweb',    'https://bbs.ruliweb.com/community'),
    ('mlbpark',    'https://mlbpark.donga.com/mp/?b=bullpen'),
    ('mlbpark',    'https://mlbpark.donga.com/mp/?b=political'),
    ('inven',      'https://www.inven.co.kr/board/it'),
    ('inven',      'https://www.inven.co.kr/board/webzine'),
    ('bobaedream', 'https://www.bobaedream.co.kr/list?code=best'),
    ('pgr21',      'https://www.pgr21.com/freedom'),
    ('slashdot',   'https://slashdot.org/'),
    ('slashdot',   'https://news.slashdot.org/'),
    ('lemmy',      'https://lemmy.world/c/news'),
    ('lemmy',      'https://lemmy.world/c/worldnews'),
    ('tildes',     'https://tildes.net/'),
    ('tildes',     'https://tildes.net/~news')
  );

-- fetcher_rules: 정확한 host_pattern set + reason 매칭 — 동일 host_pattern 의 운영자 manual row 보호
DELETE FROM fetcher_rules
WHERE host_pattern IN (
  'bbs.ruliweb.com',
  'mlbpark.donga.com',
  'www.inven.co.kr',
  'www.bobaedream.co.kr',
  'www.pgr21.com',
  'slashdot.org',
  'news.slashdot.org',
  'lemmy.world',
  'tildes.net'
)
  AND reason = 'initial seed from migration 022';
