// metrics.go 는 StageGate 의 정합성 관측 collector 입니다 (이슈 #543).
//
// 관측 대상:
//   - release 실패 / 소유권 상실 — 곧 stale 락이며, 그 URL 의 해당 stage 메시지가 재큐 경로로
//     빠지는 시작점 (이슈 #63 / #540). 로그로만 남아 빈도를 알 수 없던 지점.
//   - gate skip — 재큐가 실제로 얼마나 발생하는지. 중복 작업 비용의 상한 지표.
//
// Label 정책:
//   - stage: fetcher / parser / validator / enricher 4개 고정 — bounded.
//   - reason: 고정 상수 집합 — bounded.

package locks

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// release 실패 사유 label 값.
const (
	// ReleaseFailNotOwned — TTL 초과로 락이 만료되고 타 인스턴스가 재획득한 상태.
	// **정상 동작이지만** TTL 이 실제 처리 시간보다 짧다는 신호다.
	ReleaseFailNotOwned = "not_owned"
	// ReleaseFailInfra — Redis 장애 / timeout 등 인프라 실패. 락이 TTL 까지 남는다.
	ReleaseFailInfra = "infra"
)

// GateMetrics 는 StageGate 의 Prometheus collector 입니다.
//
// nil-GateMetrics 또는 nil 내부 collector 는 모든 Record* 가 noop — 호출자는 nil 검사 없이
// 항상 호출 가능합니다 (auto_demote_metrics / refiner.Metrics 와 동일 정책).
type GateMetrics struct {
	releaseFailed *prometheus.CounterVec // labels: stage, reason
	skipped       *prometheus.CounterVec // labels: stage
}

// NewGateMetrics 는 GateMetrics 를 생성합니다.
//
// registry 가 nil 이면 모든 collector 가 nil — METRICS 비활성 환경을 cover.
// 동일 registry 에 두 번 호출돼도 idempotent (기존 collector 재사용).
func NewGateMetrics(registry *prometheus.Registry) *GateMetrics {
	if registry == nil {
		return &GateMetrics{}
	}
	return &GateMetrics{
		releaseFailed: registerOrReuseGateCounter(registry, prometheus.CounterOpts{
			Name: "stage_gate_release_failed_total",
			Help: "StageGate lock release failures by stage and reason. not_owned means processing exceeded the lock TTL.",
		}, []string{"stage", "reason"}),
		skipped: registerOrReuseGateCounter(registry, prometheus.CounterOpts{
			Name: "stage_gate_skipped_total",
			Help: "Messages skipped because the stage lock was held by another worker, by stage.",
		}, []string{"stage"}),
	}
}

// RecordReleaseFailure 는 release 실패 1건을 기록합니다.
func (m *GateMetrics) RecordReleaseFailure(stage, reason string) {
	if m == nil || m.releaseFailed == nil {
		return
	}
	m.releaseFailed.WithLabelValues(stage, reason).Inc()
}

// RecordSkip 은 gate 선점으로 건너뛴 1건을 기록합니다.
func (m *GateMetrics) RecordSkip(stage string) {
	if m == nil || m.skipped == nil {
		return
	}
	m.skipped.WithLabelValues(stage).Inc()
}

// registerOrReuseGateCounter 는 collector 를 등록하거나 이미 등록된 것을 반환합니다.
//
// 같은 이름의 호환되지 않는 collector 와 충돌하면 등록을 포기하고 nil 반환 — metric 이름 충돌로
// 프로세스가 죽는 것보다 해당 지표만 비활성화되는 쪽이 낫다 (관측은 부가 기능).
func registerOrReuseGateCounter(registry *prometheus.Registry, opts prometheus.CounterOpts, labels []string) *prometheus.CounterVec {
	counter := prometheus.NewCounterVec(opts, labels)
	if err := registry.Register(counter); err != nil {
		var are prometheus.AlreadyRegisteredError
		if ok := asAlreadyRegistered(err, &are); ok {
			if existing, isCounter := are.ExistingCollector.(*prometheus.CounterVec); isCounter {
				return existing
			}
		}
		return nil
	}
	return counter
}

// asAlreadyRegistered 는 errors.As 의 얇은 래퍼입니다 — 등록 충돌 판별 의도를 이름으로 드러냅니다.
func asAlreadyRegistered(err error, target *prometheus.AlreadyRegisteredError) bool {
	return errors.As(err, target)
}
