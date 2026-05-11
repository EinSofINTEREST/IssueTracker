-- 025 DOWN: 라이브 #11 의 manual 우회 상태로 복원 (host 별 base_url 통일).
--
-- 본 DOWN 은 이슈 #347 의 strict check 가 다시 활성화된 환경에서만 의미 있음.
-- 현재 코드에서는 BaseURL 차이가 허용되므로 DOWN 적용 시 기능적 영향 없음 (HealthCheck 가
-- canonical 만 사용).

UPDATE fetcher_rules
   SET base_url = 'https://gall.dcinside.com'
 WHERE host_pattern = 'gallery.dcinside.com'
   AND source_name = 'dcinside'
   AND base_url IS DISTINCT FROM 'https://gall.dcinside.com';

UPDATE fetcher_rules
   SET base_url = 'https://old.reddit.com'
 WHERE host_pattern = 'www.reddit.com'
   AND source_name = 'reddit'
   AND base_url IS DISTINCT FROM 'https://old.reddit.com';

UPDATE fetcher_rules
   SET base_url = 'https://slashdot.org'
 WHERE host_pattern = 'news.slashdot.org'
   AND source_name = 'slashdot'
   AND base_url IS DISTINCT FROM 'https://slashdot.org';
