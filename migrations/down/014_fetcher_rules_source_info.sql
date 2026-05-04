-- 014_fetcher_rules_source_info down: seed row 제거 + SourceInfo·RequestsPerHour 컬럼 제거.
--
-- seed row 를 먼저 삭제한 뒤 컬럼을 DROP 해야 013 시점 상태로 완전 복원됨.
-- reason 조건으로 seed row 만 정밀 삭제 — manual/auto_upgrade row 는 유지.
DELETE FROM fetcher_rules
WHERE reason LIKE 'initial seed from sources/%';

ALTER TABLE fetcher_rules
  DROP COLUMN IF EXISTS source_name,
  DROP COLUMN IF EXISTS source_type,
  DROP COLUMN IF EXISTS country,
  DROP COLUMN IF EXISTS language,
  DROP COLUMN IF EXISTS base_url,
  DROP COLUMN IF EXISTS requests_per_hour;
