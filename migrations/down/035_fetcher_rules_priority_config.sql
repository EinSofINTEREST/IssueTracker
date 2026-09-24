-- 035_fetcher_rules_priority_config (down): 이슈 #383
--
-- 컬럼을 지우면 override 스냅샷이 비게 되고, OverridePriorityResolver 의 CanResolve 가
-- false 를 반환해 chain 이 기존 경로 (RuleBased → Scoring) 로 되돌아간다.
ALTER TABLE fetcher_rules
  DROP COLUMN IF EXISTS priority_config;
