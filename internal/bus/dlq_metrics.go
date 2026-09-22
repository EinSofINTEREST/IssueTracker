// dlq_metrics.go 는 DLQ 발행 관측 collector 입니다 (이슈 #543).
//
// DLQ 는 fetcher / validate / enrich 세 곳에서 발행되지만 소비자가 없어, 얼마나 쌓이는지
// 알 방법이 없었습니다. "처리 불가 메시지는 DLQ 로 격리한다" 는 운영 서술을 수치로 뒷받침하고,
// 급증 시 알림을 걸 수 있게 합니다.
//
// 계측 위치: Publisher.Forward — 모든 DLQ 발행이 지나는 단일 I/O 지점입니다. 세 worker 의
// sendToDLQ 를 각각 계측하는 대신 여기 한 곳에 두어 중복과 배선을 줄였습니다.
//
// Label 정책:
//   - origin: DLQ 메시지의 "original-topic" 헤더 = 어느 stage 의 입력 토픽에서 왔는지.
//     토픽 상수 집합이라 bounded.

package bus

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// dlqOriginUnknown 은 original-topic 헤더가 없을 때 쓰는 label 값입니다.
// 헤더는 세 발행 지점 모두 채우지만, 외부에서 직접 DLQ 로 Forward 하는 경로를 대비합니다.
const dlqOriginUnknown = "unknown"

// DLQMetrics 는 DLQ 발행 Prometheus collector 입니다.
//
// nil-DLQMetrics 또는 nil 내부 collector 는 Record 가 noop — 호출자는 nil 검사 불필요.
type DLQMetrics struct {
	published *prometheus.CounterVec // labels: origin
}

// NewDLQMetrics 는 DLQMetrics 를 생성합니다. registry 가 nil 이면 noop collector.
func NewDLQMetrics(registry *prometheus.Registry) *DLQMetrics {
	if registry == nil {
		return &DLQMetrics{}
	}
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dlq_published_total",
		Help: "Messages published to the dead letter queue, labeled by the topic they came from.",
	}, []string{"origin"})

	if err := registry.Register(counter); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
				return &DLQMetrics{published: existing}
			}
		}
		// 이름 충돌 — 지표만 비활성화하고 진행 (관측은 부가 기능, 프로세스를 죽이지 않는다).
		return &DLQMetrics{}
	}
	return &DLQMetrics{published: counter}
}

// RecordPublished 는 DLQ 발행 1건을 기록합니다.
func (m *DLQMetrics) RecordPublished(origin string) {
	if m == nil || m.published == nil {
		return
	}
	if origin == "" {
		origin = dlqOriginUnknown
	}
	m.published.WithLabelValues(origin).Inc()
}
