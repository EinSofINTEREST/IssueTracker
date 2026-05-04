-- 014_fetcher_rules_source_info: fetcher_rules 에 SourceInfo·RequestsPerHour 컬럼 추가 + 초기 seed (이슈 #245)
--
-- 배경:
--   사이트별 ChainHandler(#233) 통합을 위해 GenericChainHandler 가 host 단위로 SourceInfo와
--   rate limit 설정을 조회해야 한다. 현재 코드에 하드코딩된 값을 fetcher_rules 테이블로 이관.
--
-- 컬럼 추가:
--   source_name, source_type, country, language, base_url : NULL 허용 — 기존 행 호환
--   requests_per_hour                                     : NOT NULL DEFAULT 0

ALTER TABLE fetcher_rules
  ADD COLUMN IF NOT EXISTS source_name        TEXT,
  ADD COLUMN IF NOT EXISTS source_type        TEXT CHECK (source_type IS NULL OR source_type IN ('news', 'community', 'social')),
  ADD COLUMN IF NOT EXISTS country            CHAR(2),
  ADD COLUMN IF NOT EXISTS language           CHAR(2),
  ADD COLUMN IF NOT EXISTS base_url           TEXT,
  ADD COLUMN IF NOT EXISTS requests_per_hour  INT  NOT NULL DEFAULT 0 CHECK (requests_per_hour >= 0);

COMMENT ON COLUMN fetcher_rules.source_name       IS '소스 식별자 (예: naver, cnn). GenericChainHandler 가 CrawlJob.CrawlerName 에 설정.';
COMMENT ON COLUMN fetcher_rules.source_type       IS '소스 분류: news | community | social.';
COMMENT ON COLUMN fetcher_rules.country           IS 'ISO 3166-1 alpha-2. SourceInfo.Country.';
COMMENT ON COLUMN fetcher_rules.language          IS 'ISO 639-1. SourceInfo.Language.';
COMMENT ON COLUMN fetcher_rules.base_url          IS '소스 기준 URL. SourceInfo.BaseURL.';
COMMENT ON COLUMN fetcher_rules.requests_per_hour IS '시간당 최대 요청 수. 0 = 제한 없음.';

-- 초기 seed: sources/{kr,us}/*/config.go 에서 이관
-- fetcher 컬럼 기본값은 'goquery' (현재 코드 동작과 동일)
--
-- ON CONFLICT 정책:
--   - SourceInfo 컬럼(source_name ~ requests_per_hour)만 갱신.
--   - fetcher, reason 은 의도적으로 갱신하지 않음:
--       fetcher : auto-upgrade(chromedp) 설정이 seed 에 의해 덮어쓰이지 않도록 보호.
--       reason  : 'auto_upgrade_validation' 인 row 가 'initial seed...' 로 overwrite 되면
--                 BulkDowngradeAutoUpgraded (WHERE reason='auto_upgrade_validation') 조건에서
--                 해당 host 가 영구 누락됨 — CodeRabbit Major 피드백 반영.
INSERT INTO fetcher_rules (host_pattern, fetcher, source_name, source_type, country, language, base_url, requests_per_hour, reason)
VALUES
  ('news.naver.com',  'goquery', 'naver',  'news', 'KR', 'ko', 'https://news.naver.com',  200, 'initial seed from sources/kr/naver/config.go'),
  ('n.news.naver.com','goquery', 'naver',  'news', 'KR', 'ko', 'https://news.naver.com',  200, 'initial seed from sources/kr/naver/config.go — article subdomain'),
  ('news.daum.net',   'goquery', 'daum',   'news', 'KR', 'ko', 'https://news.daum.net',   200, 'initial seed from sources/kr/daum/config.go'),
  ('v.daum.net',      'goquery', 'daum',   'news', 'KR', 'ko', 'https://news.daum.net',   200, 'initial seed from sources/kr/daum/config.go — article subdomain'),
  ('www.yna.co.kr',   'goquery', 'yonhap', 'news', 'KR', 'ko', 'https://www.yna.co.kr',   100, 'initial seed from sources/kr/yonhap/config.go'),
  ('www.cnn.com',     'goquery', 'cnn',    'news', 'US', 'en', 'https://www.cnn.com',      100, 'initial seed from sources/us/cnn/config.go'),
  ('edition.cnn.com', 'goquery', 'cnn',    'news', 'US', 'en', 'https://www.cnn.com',      100, 'initial seed from sources/us/cnn/config.go — edition subdomain')
ON CONFLICT (host_pattern) DO UPDATE
  SET source_name        = EXCLUDED.source_name,
      source_type        = EXCLUDED.source_type,
      country            = EXCLUDED.country,
      language           = EXCLUDED.language,
      base_url           = EXCLUDED.base_url,
      requests_per_hour  = EXCLUDED.requests_per_hour,
      updated_at         = NOW();
