-- 034_host_scoring_state: host 단위 동적 priority score 상태 (이슈 #382, 메타 #380 Sub B)
--
-- 배경:
--   priority chain 은 현재 Explicit → Source → RuleBased(DB host/path) → default Normal.
--   RuleBased 는 운영자가 fetcher_rules/parser_rules 에 crawl_priority 를 **명시한** host 만
--   분기하므로, 미지정 host 는 전부 Normal 로 흐른다. 본 테이블은 관찰된 signal 로
--   host 별 점수를 누적해 High ↔ Normal 을 자동 분기하기 위한 상태 저장소다.
--
-- 범위 (이슈 #382 결정 — 3 signal 로 시작):
--   - freshness   : publish → detect lag (contents 의 published_at vs created_at)
--   - impact      : 카테고리 가중치
--   - host_trust  : validation 통과율
--
--   cost / reliability 는 제외한다. 두 신호의 출처인 Prometheus metrics 는 앱에 읽기 경로가
--   없고, 필요한 것은 현재 값이 아니라 **구간 rate** 이며, 인스턴스 로컬이라 여러 인스턴스가
--   뜨면 host 점수가 갈린다 (이슈 #289 와 얽힘). 골격을 먼저 세우고 그 결정 후 얹는다.
--
-- 스키마 의도:
--   - signals JSONB — 입력값 스냅샷. 점수만 남기면 "왜 그 점수인지" 를 사후에 알 수 없어
--     운영자가 weight 를 조정할 근거가 사라진다.
--   - window_minutes — 집계 구간. weight 를 바꿔 재계산할 때 같은 구간인지 확인용.
--   - score 는 [0,1] 정규화. CHECK 로 범위를 강제해 계산 버그가 조용히 저장되지 않게 한다.
--
-- 호환성:
--   - 신규 테이블 — 기존 동작에 영향 없음.
--   - 본 테이블이 비어 있으면 DynamicScorePriorityResolver 의 CanResolve 가 false 를
--     반환해 chain 이 기존 경로로 위임한다 (cold-start = 기존 동작).

CREATE TABLE IF NOT EXISTS host_scoring_state (
  host            TEXT PRIMARY KEY,
  score           REAL NOT NULL,
  signals         JSONB NOT NULL DEFAULT '{}'::JSONB,
  window_minutes  INT NOT NULL,
  calculated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  CONSTRAINT host_scoring_state_score_range CHECK (score >= 0.0 AND score <= 1.0),
  CONSTRAINT host_scoring_state_window_positive CHECK (window_minutes > 0)
);

-- scorer 가 오래된 row 부터 갱신하거나 stale 판정할 때 사용.
-- resolver 는 PK lookup 만 하므로 별도 인덱스 불필요.
CREATE INDEX IF NOT EXISTS idx_host_scoring_state_calculated_at
  ON host_scoring_state (calculated_at);

COMMENT ON TABLE host_scoring_state IS
  'host 단위 동적 priority score (이슈 #382). High ↔ Normal 분기에만 사용 — Low 는 대상 아님';
COMMENT ON COLUMN host_scoring_state.signals IS
  '입력 signal 스냅샷 — 점수의 근거. weight 재조정 시 판단 자료';
