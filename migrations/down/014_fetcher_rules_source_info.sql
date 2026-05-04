-- 014_fetcher_rules_source_info down: SourceInfo·RequestsPerHour 컬럼 제거.
ALTER TABLE fetcher_rules
  DROP COLUMN IF EXISTS source_name,
  DROP COLUMN IF EXISTS source_type,
  DROP COLUMN IF EXISTS country,
  DROP COLUMN IF EXISTS language,
  DROP COLUMN IF EXISTS base_url,
  DROP COLUMN IF EXISTS requests_per_hour;
