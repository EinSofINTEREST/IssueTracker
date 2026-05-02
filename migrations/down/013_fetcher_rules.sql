-- 013_fetcher_rules down: 호스트 단위 fetcher 선택 정책 테이블 제거.
DROP INDEX IF EXISTS idx_fetcher_rules_host;
DROP TABLE IF EXISTS fetcher_rules;
