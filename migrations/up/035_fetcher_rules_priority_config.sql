-- 035_fetcher_rules_priority_config: host 단위 priority override + weight (이슈 #383, 메타 #380 Sub C)
--
-- 배경:
--   Sub B (이슈 #382) 의 scoring 은 cluster-wide weight 와 단일 threshold 를 쓴다. host 마다
--   특성이 달라 동일 weight 가 부정확하고, 운영자가 특정 host 를 일시적으로 High 로 올리고
--   싶은 케이스 (이벤트 발생 등) 를 표현할 수단이 없다.
--
-- 왜 fetcher_rules 인가:
--   host 단위 운영 인터페이스를 한 테이블로 모은다. 이미 host_pattern 이 UNIQUE PK 역할을
--   하고 운영자가 fetcher 선택을 여기서 관리하므로, priority 도 같은 row 에 두면 host 하나를
--   볼 때 한 곳만 보면 된다.
--
-- JSON 스키마 (모든 키 optional — 부분 override 가능):
--   {
--     "base_priority":   "high" | "normal" | "low",
--     "override_until":  "2026-05-20T00:00:00Z",   -- RFC3339. 없으면 무기한
--     "signal_weights":  {"freshness": 1.5, "impact": 1.0, "host_trust": 1.2},
--     "score_threshold": 0.55
--   }
--
--   signal_weights 는 **Sub B 가 실제로 쓰는 3개 키만** 둔다 (freshness / impact / host_trust).
--   원 설계의 cost / reliability 는 Sub B 에서 제외됐으므로 (metrics 읽기 경로 부재 +
--   인스턴스 로컬 문제, 이슈 #382) 지금 넣으면 **설정해도 조용히 무시되는 키** 가 된다.
--   필요해지면 JSONB 라 추가가 쉽다.
--
-- 호환성:
--   - NULL 허용 + DEFAULT 없음 — 기존 row 는 그대로 두고, 설정한 host 만 override 적용.
--   - 값 검증은 애플리케이션이 한다. CHECK 로 JSON 구조를 강제하면 스키마 확장 때마다
--     마이그레이션이 필요해지고, 잘못된 설정의 진단 메시지도 DB 에러라 불친절하다.

ALTER TABLE fetcher_rules
  ADD COLUMN IF NOT EXISTS priority_config JSONB;

COMMENT ON COLUMN fetcher_rules.priority_config IS
  'host 단위 priority override + signal weight (이슈 #383). NULL = override 없음 (기본 동작)';
