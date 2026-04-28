package worker

import (
	"fmt"
	"sync"
	"time"

	"issuetracker/pkg/logger"
)

// cbState는 circuit breaker 상태를 나타냅니다.
type cbState int

const (
	// cbStateClosed: 정상 동작 상태. 실패 횟수를 누적합니다.
	cbStateClosed cbState = iota
	// cbStateOpen: 차단 상태. 모든 요청을 즉시 거부합니다.
	// OpenTimeout 경과 후 HalfOpen으로 전환됩니다.
	cbStateOpen
	// cbStateHalfOpen: 탐색 상태. 단일 probe 요청만 허용합니다.
	// 성공 시 Closed, 실패 시 Open으로 전환됩니다.
	cbStateHalfOpen
)

func (s cbState) String() string {
	switch s {
	case cbStateClosed:
		return "closed"
	case cbStateOpen:
		return "open"
	case cbStateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen은 circuit breaker가 open 상태일 때 반환되는 에러입니다.
type ErrCircuitOpen struct {
	Source string
}

func (e *ErrCircuitOpen) Error() string {
	return fmt.Sprintf("circuit breaker open for source %q", e.Source)
}

// CircuitBreakerConfig는 circuit breaker 설정입니다.
type CircuitBreakerConfig struct {
	// MaxFailures: Open 전환 기준 연속 실패 횟수
	MaxFailures int
	// OpenTimeout: Open 상태 유지 시간. 경과 후 HalfOpen으로 전환됩니다.
	OpenTimeout time.Duration
}

// DefaultCircuitBreakerConfig는 기본 circuit breaker 설정입니다.
var DefaultCircuitBreakerConfig = CircuitBreakerConfig{
	MaxFailures: 5,
	OpenTimeout: 60 * time.Second,
}

// CircuitBreaker는 단일 소스에 대한 circuit breaker입니다.
// goroutine-safe합니다.
type CircuitBreaker struct {
	config CircuitBreakerConfig
	source string
	log    *logger.Logger // 이슈 #137 — state 전이 가시성용 (nil 허용 — 미주입 시 로그 skip)

	mu          sync.Mutex
	state       cbState
	failures    int       // Closed 상태의 연속 실패 횟수
	openedAt    time.Time // Open 전환 시각
	probeActive bool      // HalfOpen probe 진행 중 여부
}

func newCircuitBreaker(source string, config CircuitBreakerConfig, log *logger.Logger) *CircuitBreaker {
	return &CircuitBreaker{
		config: config,
		source: source,
		log:    log,
		state:  cbStateClosed,
	}
}

// Allow는 요청 허용 여부를 반환합니다.
// HalfOpen에서 probe가 이미 진행 중이면 false를 반환합니다.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case cbStateClosed:
		return true

	case cbStateOpen:
		// OpenTimeout 경과 시 HalfOpen으로 전환하여 probe 허용
		if time.Since(cb.openedAt) >= cb.config.OpenTimeout {
			openFor := time.Since(cb.openedAt)
			cb.state = cbStateHalfOpen
			cb.probeActive = true
			cb.logTransition("open", "half_open", map[string]interface{}{
				"open_for_ms": openFor.Milliseconds(),
			}, false)
			return true
		}
		return false

	case cbStateHalfOpen:
		// probe가 이미 진행 중이면 나머지 요청은 차단
		if cb.probeActive {
			return false
		}
		cb.probeActive = true
		return true

	default:
		return true
	}
}

// RecordSuccess는 성공을 기록합니다.
// HalfOpen 상태면 Closed로 전환합니다.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures = 0
	if cb.state == cbStateHalfOpen {
		cb.state = cbStateClosed
		cb.probeActive = false
		cb.logTransition("half_open", "closed", nil, false)
	}
}

// RecordFailure는 실패를 기록합니다.
// Closed에서 MaxFailures 초과 시 Open으로 전환합니다.
// HalfOpen probe 실패 시 즉시 Open으로 전환합니다.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case cbStateClosed:
		cb.failures++
		if cb.failures >= cb.config.MaxFailures {
			cb.state = cbStateOpen
			cb.openedAt = time.Now()
			cb.logTransition("closed", "open", map[string]interface{}{
				"failures":     cb.failures,
				"max_failures": cb.config.MaxFailures,
			}, true)
		}

	case cbStateHalfOpen:
		// probe 실패 → 즉시 다시 Open
		cb.state = cbStateOpen
		cb.openedAt = time.Now()
		cb.probeActive = false
		cb.logTransition("half_open", "open", nil, true)
	}
}

// logTransition 은 state 전이를 구조화 로그로 출력합니다 (이슈 #137).
//
// asWarn=true 면 WARN, false 면 INFO 레벨로 출력합니다 — open 으로 들어가는 전이는
// 운영 알림 가치가 있어 WARN, 그 외 (probing/recovered) 는 INFO. logger 미주입 시 no-op.
//
// 호출자는 cb.mu 를 이미 보유하고 있어야 합니다 — 이 메소드는 추가 lock 을 잡지 않습니다.
func (cb *CircuitBreaker) logTransition(from, to string, extra map[string]interface{}, asWarn bool) {
	if cb.log == nil {
		return
	}
	fields := map[string]interface{}{
		"source":     cb.source,
		"from_state": from,
		"to_state":   to,
	}
	for k, v := range extra {
		fields[k] = v
	}
	entry := cb.log.WithFields(fields)
	if asWarn {
		entry.Warn("circuit breaker state transition")
	} else {
		entry.Info("circuit breaker state transition")
	}
}

// State는 현재 상태를 반환합니다. 테스트 및 모니터링용입니다.
func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state.String()
}

// Failures는 현재 연속 실패 횟수를 반환합니다. 테스트 및 모니터링용입니다.
func (cb *CircuitBreaker) Failures() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.failures
}

// CircuitBreakerRegistry는 소스별 CircuitBreaker를 관리합니다.
// goroutine-safe합니다.
type CircuitBreakerRegistry struct {
	config CircuitBreakerConfig
	log    *logger.Logger // 이슈 #137 — 신규 CB 생성 시 주입 (nil 허용)
	mu     sync.RWMutex
	cbs    map[string]*CircuitBreaker
}

// NewCircuitBreakerRegistry는 CircuitBreakerRegistry를 생성합니다.
// log 가 nil 이 아니면 각 CB 의 state 전이 로그가 출력됩니다 (이슈 #137).
func NewCircuitBreakerRegistry(config CircuitBreakerConfig, log *logger.Logger) *CircuitBreakerRegistry {
	return &CircuitBreakerRegistry{
		config: config,
		log:    log,
		cbs:    make(map[string]*CircuitBreaker),
	}
}

// Get은 소스에 해당하는 CircuitBreaker를 반환합니다.
// 없으면 새로 생성합니다.
func (r *CircuitBreakerRegistry) Get(source string) *CircuitBreaker {
	r.mu.RLock()
	cb, ok := r.cbs[source]
	r.mu.RUnlock()
	if ok {
		return cb
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// double-checked locking
	if cb, ok = r.cbs[source]; ok {
		return cb
	}
	cb = newCircuitBreaker(source, r.config, r.log)
	r.cbs[source] = cb
	return cb
}
