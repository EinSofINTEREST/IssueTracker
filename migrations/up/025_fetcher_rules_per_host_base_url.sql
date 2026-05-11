-- 025_fetcher_rules_per_host_base_url: PR #334/#335 의도 복원 — host 별 자기 base_url (이슈 #347)
--
-- 배경:
--   PR #334 (dcinside dual-host) / PR #335 (slashdot/reddit dual-host) 가 host_pattern 별
--   자기 base_url 을 seed 했으나, RegisterAll 의 strict consistency check 가 BaseURL 차이도
--   거부 → boot fail. 즉시 우회로 운영자가 base_url 을 source_name 별 단일 값으로 통일하는
--   manual SQL 실행 (라이브 #11). 본 마이그레이션은 #347 PR 에서 strict check 완화 후의
--   정합 상태 복원.
--
-- 변경:
--   - dcinside: gallery.dcinside.com 의 base_url → https://gallery.dcinside.com
--   - reddit:   www.reddit.com 의 base_url → https://www.reddit.com
--   - slashdot: news.slashdot.org 의 base_url → https://news.slashdot.org
--
-- 동작 영향:
--   본 base_url 은 GoQuery/Generic HealthCheck 외에는 runtime 영향이 없음 (이슈 #347 분석).
--   canonical 선택 (isCanonicalHost) 이 HealthCheck 용 단일 base_url 을 결정하므로 다른 host
--   의 base_url 은 기능적으로 무시되지만, DB 상 의도된 host-specific 값으로 정합 보존.
--
-- 멱등:
--   각 UPDATE 는 host_pattern 으로 정확히 식별. 이미 해당 값이면 변경 없음.

UPDATE fetcher_rules
   SET base_url = 'https://gallery.dcinside.com'
 WHERE host_pattern = 'gallery.dcinside.com'
   AND source_name = 'dcinside'
   AND base_url <> 'https://gallery.dcinside.com';

UPDATE fetcher_rules
   SET base_url = 'https://www.reddit.com'
 WHERE host_pattern = 'www.reddit.com'
   AND source_name = 'reddit'
   AND base_url <> 'https://www.reddit.com';

UPDATE fetcher_rules
   SET base_url = 'https://news.slashdot.org'
 WHERE host_pattern = 'news.slashdot.org'
   AND source_name = 'slashdot'
   AND base_url <> 'https://news.slashdot.org';
