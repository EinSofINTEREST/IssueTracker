-- 034_host_scoring_state (down): 이슈 #382
--
-- 테이블을 지우면 DynamicScorePriorityResolver 의 lookup 이 실패하지만, 그 경로는
-- 조회 실패를 CanResolve=false 로 흡수하므로 chain 이 기존 동작으로 되돌아간다.
DROP INDEX IF EXISTS idx_host_scoring_state_calculated_at;
DROP TABLE IF EXISTS host_scoring_state;
