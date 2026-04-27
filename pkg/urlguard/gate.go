package urlguard

import (
	"context"

	"issuetracker/pkg/logger"
)

// Gate 는 Guard 결과를 일관된 방식으로 적용·로깅하는 단일 공통 컴포넌트입니다.
//
// 본 패키지의 핵심 — Scheduler / Publisher / Consumer 등 모든 진입점이 동일한
// Gate 인스턴스의 메서드를 호출하여 차단 정책을 적용합니다. 레이어별로 별도의
// 데코레이터 타입을 만들지 않고, "decorator 처럼 동작" 하는 단일 helper 가 모든
// 곳에서 재사용되는 형태.
//
// 두 가지 사용 패턴:
//   - Allow(url, fields)   — 단일 URL 검사 (Scheduler/Worker 처럼 1건 처리)
//   - Filter(urls, fields) — URL 슬라이스 필터링 (Publisher 처럼 다건 처리)
//
// 두 메서드 모두 차단 시 동일한 WARN 로그 형식을 사용 (url/reason 필드 + 호출자
// 컨텍스트 필드) — 운영 모니터링에서 차단 이벤트를 일관되게 식별 가능.
//
// goroutine-safe — 내부 상태가 없으며 Guard 와 Logger 는 둘 다 race-safe 가정.
type Gate struct {
	guard Guard
	log   *logger.Logger
}

// NewGate 는 주어진 guard 와 log 로 새 Gate 를 생성합니다.
// guard 가 nil 이면 panic — 비활성화는 AllowAllGuard{} 명시 주입으로 표현.
// log 가 nil 이면 logger.FromContext(context.Background()) 의 기본 logger 를 사용.
func NewGate(guard Guard, log *logger.Logger) *Gate {
	if guard == nil {
		panic("urlguard: NewGate requires non-nil Guard (use AllowAllGuard{} to disable)")
	}
	if log == nil {
		log = logger.FromContext(context.Background())
	}
	return &Gate{guard: guard, log: log}
}

// Allow 는 단일 URL 이 통과하는지 검사합니다.
//
// 통과(true): WARN 로그 없음.
// 차단(false): "blocked by url guard" WARN 로그 + 호출자 fields + url/reason 자동 포함.
//
// fields 는 호출자 컨텍스트 (예: {"crawler":"cnn", "job_id":"...", "stage":"scheduler"})
// 를 전달합니다. nil 가능 — 그 경우 url/reason 만 로깅.
func (g *Gate) Allow(url string, fields map[string]interface{}) bool {
	allowed, reason := g.guard.Allow(url)
	if allowed {
		return true
	}

	merged := make(map[string]interface{}, len(fields)+2)
	for k, v := range fields {
		merged[k] = v
	}
	merged["url"] = url
	merged["reason"] = reason
	g.log.WithFields(merged).Warn("blocked by url guard")
	return false
}

// Filter 는 슬라이스에서 통과 URL 만 추려 반환합니다. 차단된 각 URL 에 대해
// Allow 와 동일한 WARN 로그를 남깁니다.
//
// 입력이 빈 슬라이스 또는 nil 이면 빈 슬라이스를 반환합니다.
// 모두 차단되어도 nil 이 아닌 빈 슬라이스를 반환 — 호출자가 len() 으로 일관 분기 가능.
//
// 호출자가 결과 슬라이스를 수정해도 입력 슬라이스에 영향이 없도록 새 슬라이스를 할당합니다.
func (g *Gate) Filter(urls []string, fields map[string]interface{}) []string {
	out := make([]string, 0, len(urls))
	for _, url := range urls {
		if g.Allow(url, fields) {
			out = append(out, url)
		}
	}
	return out
}
