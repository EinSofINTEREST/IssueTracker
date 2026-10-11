package api

import (
	"context"
	"net/http"
	"time"

	"issuetracker/pkg/logger"
)

// degradedLatency: DB ping 이 이 시간을 넘으면 healthy 대신 degraded 로 보고합니다
// (04-error-handling.md 의 DatabaseHealthChecker 기준과 동일).
const degradedLatency = 100 * time.Millisecond

// pingTimeout: health 응답이 DB 지연에 끌려가지 않도록 ping 자체에 거는 상한.
//
// 이 값을 넘으면 unhealthy 로 보고합니다 — health endpoint 가 먹통이 되면 오케스트레이터가
// 살아있는지 죽었는지 판단할 수 없게 되므로, 느린 DB 보다 빠른 실패가 낫습니다.
const pingTimeout = 2 * time.Second

// HealthState 는 컴포넌트 상태입니다.
type HealthState string

const (
	// StateHealthy: 정상.
	StateHealthy HealthState = "healthy"
	// StateDegraded: 동작하지만 느림 — 서비스는 계속 제공.
	StateDegraded HealthState = "degraded"
	// StateUnhealthy: 사용 불가.
	StateUnhealthy HealthState = "unhealthy"
)

// Pinger 는 health 체크가 의존하는 최소 연산입니다.
// pgxpool.Pool 이 구조적으로 만족하며, 테스트는 stub 으로 교체합니다.
type Pinger interface {
	Ping(ctx context.Context) error
}

// ComponentHealth 는 단일 의존성의 상태입니다.
type ComponentHealth struct {
	Status    HealthState `json:"status"`
	LatencyMs int64       `json:"latency_ms"`
	Message   string      `json:"message,omitempty"`
	CheckedAt time.Time   `json:"checked_at"`
}

// HealthResponse 는 /health 응답입니다.
//
// Kafka 는 포함하지 않습니다 — API 서버는 producer/consumer 가 아니므로 쓰지 않는
// 의존성의 상태를 보고하면 장애 판단을 오도합니다 (이슈 #635).
type HealthResponse struct {
	Status     HealthState                `json:"status"`
	Components map[string]ComponentHealth `json:"components"`
}

// NewHealthHandler 는 DB 연결 상태를 확인하는 /health 핸들러를 반환합니다.
//
// 상태 코드: healthy / degraded 는 200, unhealthy 는 503.
// degraded 를 200 으로 두는 이유 — 느릴 뿐 요청은 처리되므로 로드밸런서가 인스턴스를
// 빼면 오히려 남은 인스턴스의 부하가 늘어 상황이 악화됩니다.
func NewHealthHandler(db Pinger, log *logger.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		component := checkDatabase(r.Context(), db)

		resp := HealthResponse{
			Status:     component.Status,
			Components: map[string]ComponentHealth{"database": component},
		}

		status := http.StatusOK
		if component.Status == StateUnhealthy {
			status = http.StatusServiceUnavailable
		}

		WriteJSON(w, log, status, resp)
	}
}

// checkDatabase 는 DB ping 결과를 ComponentHealth 로 환산합니다.
func checkDatabase(ctx context.Context, db Pinger) ComponentHealth {
	checkedAt := time.Now()

	if db == nil {
		return ComponentHealth{
			Status:    StateUnhealthy,
			Message:   "database not configured",
			CheckedAt: checkedAt,
		}
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	start := time.Now()
	err := db.Ping(pingCtx)
	latency := time.Since(start)

	if err != nil {
		// 에러 문자열은 접속 정보를 담을 수 있어 그대로 싣지 않습니다.
		return ComponentHealth{
			Status:    StateUnhealthy,
			LatencyMs: latency.Milliseconds(),
			Message:   "database ping failed",
			CheckedAt: checkedAt,
		}
	}

	state := StateHealthy
	if latency > degradedLatency {
		state = StateDegraded
	}

	return ComponentHealth{
		Status:    state,
		LatencyMs: latency.Milliseconds(),
		CheckedAt: checkedAt,
	}
}
