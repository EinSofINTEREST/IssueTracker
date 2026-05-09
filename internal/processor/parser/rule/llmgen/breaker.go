package llmgen

import (
	"sync"
	"time"

	"issuetracker/internal/storage"
)

// HostBreakerConfig 는 HostBreaker 의 동작 정책입니다.
//
// FailureThreshold: 연속 rate_limit hit 가 몇 번이면 cooldown 진입할지.
// CooldownDuration: cooldown 진입 시 차단 유지 시간.
//
// 기본 정책 (.claude/rules/04-error-handling.md, 이슈 #215):
//
//	FailureThreshold = 3, CooldownDuration = 10 * time.Minute
type HostBreakerConfig struct {
	FailureThreshold int
	CooldownDuration time.Duration
}

// DefaultHostBreakerConfig 는 #215 의 기본 정책을 반환합니다.
func DefaultHostBreakerConfig() HostBreakerConfig {
	return HostBreakerConfig{
		FailureThreshold: 3,
		CooldownDuration: 10 * time.Minute,
	}
}

// hostKey 는 (host, target_type) 단위 breaker state 의 키입니다.
//
// target_type 별로 분리 — page/list 가 동일 host 에서도 별도의 LLM 호출이며 각각의 quota
// 소비 패턴이 다르므로 cooldown 도 분리.
type hostKey struct {
	host       string
	targetType storage.TargetType
}

// hostState 는 단일 (host, target_type) 의 breaker 상태입니다.
//
// failures: 연속 rate_limit hit 카운터 (success 또는 non-rate_limit error 시 reset).
// blockedUntil: cooldown 종료 시각. zero 면 cooldown 아님.
type hostState struct {
	failures     int
	blockedUntil time.Time
}

// HostBreaker 는 (host, target_type) 단위로 rate_limit 연속 발생 시 cooldown 차단을
// 관리하는 circuit breaker 입니다 (이슈 #215).
//
// 사용 흐름 (Generator 의 LLM 호출 wrapper):
//  1. Allow(host, type) 호출 — false 면 호출 skip + 다음 host 처리
//  2. true 면 LLM 호출 실행
//  3. 결과에 따라:
//     - rate_limit 에러   → RecordRateLimit(host, type)
//     - 그 외 에러 / 성공 → RecordSuccess(host, type)
//
// 동시성 안전 — 단일 mu 가 모든 state 보호.
type HostBreaker struct {
	cfg HostBreakerConfig
	mu  sync.Mutex
	st  map[hostKey]*hostState
	now func() time.Time // 테스트 주입용
}

// NewHostBreaker 는 HostBreaker 를 생성합니다.
//
// cfg.FailureThreshold <= 0 또는 cfg.CooldownDuration <= 0 이면 default 적용.
func NewHostBreaker(cfg HostBreakerConfig) *HostBreaker {
	def := DefaultHostBreakerConfig()
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = def.FailureThreshold
	}
	if cfg.CooldownDuration <= 0 {
		cfg.CooldownDuration = def.CooldownDuration
	}
	return &HostBreaker{
		cfg: cfg,
		st:  make(map[hostKey]*hostState),
		now: time.Now,
	}
}

// Allow 는 (host, target_type) 가 현재 호출 허용 상태인지 반환합니다.
//
// cooldown 만료 시 entry 삭제 — unbounded map growth 방지 (CodeRabbit Major 반영).
// 만료 시점 비교는 inclusive — now == blockedUntil 도 통과로 간주 (one-tick false block 방지).
//
// 비차단 상태 (blockedUntil.IsZero) 의 entry 는 consecutive failure count 보존을 위해 삭제하지
// 않음 — 그 카운터가 0 으로 되돌아가면 threshold 도달이 안 되어 cooldown 자체가 발화 안 함.
// 비차단 entry 의 정리는 RecordSuccess (회복) 또는 cooldown 진입 후 만료 시점에 발생.
func (b *HostBreaker) Allow(host string, targetType storage.TargetType) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := hostKey{host: host, targetType: targetType}
	st, ok := b.st[key]
	if !ok {
		return true, 0
	}
	if st.blockedUntil.IsZero() {
		// 비차단 — failure count 만 누적된 상태.
		return true, 0
	}
	now := b.now()
	if !now.Before(st.blockedUntil) {
		// cooldown 만료 — stale state 제거 (다음 실패는 새 entry 로 시작).
		delete(b.st, key)
		return true, 0
	}
	return false, st.blockedUntil.Sub(now)
}

// RecordRateLimit 는 (host, target_type) 에서 rate_limit 발생을 기록합니다.
//
// failures 카운터 +1. threshold 도달 시 cooldown 진입 (blockedUntil = now + cooldown).
func (b *HostBreaker) RecordRateLimit(host string, targetType storage.TargetType) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := hostKey{host, targetType}
	st, ok := b.st[key]
	if !ok {
		st = &hostState{}
		b.st[key] = st
	}
	st.failures++
	if st.failures >= b.cfg.FailureThreshold {
		st.blockedUntil = b.now().Add(b.cfg.CooldownDuration)
	}
}

// RecordSuccess 는 (host, target_type) 에서 성공 / non-rate_limit 종료를 기록합니다.
//
// 같은 host 의 정상 응답이 한 번이라도 들어오면 entry 자체를 삭제 — failures/cooldown reset
// 효과 + map 무한 성장 방지 (CodeRabbit Major 반영). 다음 실패는 새 entry 로 시작.
func (b *HostBreaker) RecordSuccess(host string, targetType storage.TargetType) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.st, hostKey{host: host, targetType: targetType})
}

// Snapshot 은 디버깅 / 테스트용 state snapshot 입니다 (현재 차단 중인 host 목록).
//
// 운영 metric 으로 활용 가능 — 차단 host 수가 갑자기 늘면 LLM provider 한도 / 키 만료 신호.
type BreakerSnapshot struct {
	Host         string
	TargetType   storage.TargetType
	Failures     int
	BlockedUntil time.Time
}

// Snapshot 은 모든 host state 를 복사하여 반환합니다.
func (b *HostBreaker) Snapshot() []BreakerSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]BreakerSnapshot, 0, len(b.st))
	for k, st := range b.st {
		out = append(out, BreakerSnapshot{
			Host:         k.host,
			TargetType:   k.targetType,
			Failures:     st.failures,
			BlockedUntil: st.blockedUntil,
		})
	}
	return out
}
