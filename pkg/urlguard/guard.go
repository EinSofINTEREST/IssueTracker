// Package urlguard 는 URL 처리 가능 여부를 판정하는 술어(Guard) 와, 그 결과를 일관된
// 방식으로 적용·로깅하는 단일 공통 컴포넌트(Gate) 를 제공합니다.
//
// Scheduler / Publisher / Consumer 등 시스템의 여러 진입점에서 동일한 Guard 와
// Gate 를 공유하여 차단 정책을 일관되게 적용합니다 — 레이어별 별도 데코레이터를
// 만들지 않고, 각 레이어가 동일한 Gate 인스턴스의 Allow/Filter 를 호출합니다.
//
// 사용 예:
//
//	gate := urlguard.NewGate(urlguard.Default(), log)
//	scheduler.SetGate(gate)
//	publisher.SetGate(gate)
//	pool.SetGate(gate)
package urlguard

// Guard 는 URL 이 시스템에서 처리 가능한지 판정하는 술어입니다.
// 구현체는 goroutine-safe 해야 합니다 (Gate 가 동시 호출됨).
type Guard interface {
	// Allow 는 url 을 허용할지 여부와, 차단된 경우의 사유 문자열을 반환합니다.
	// allowed=true 시 reason 은 빈 문자열입니다.
	Allow(url string) (allowed bool, reason string)
}

// AllowAllGuard 는 모든 URL 을 허용하는 no-op 구현입니다.
// 가드 비활성화 또는 테스트 시 사용합니다.
type AllowAllGuard struct{}

// Allow 는 항상 (true, "") 를 반환합니다.
func (AllowAllGuard) Allow(_ string) (bool, string) {
	return true, ""
}
