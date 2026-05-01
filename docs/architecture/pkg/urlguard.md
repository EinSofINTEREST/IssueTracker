# pkg/urlguard — URL Guard & Gate

소스: [`pkg/urlguard/`](../../../pkg/urlguard/)

URL 처리 가능 여부를 판정하는 술어 (`Guard`) 와, 그 결과를 일관되게 적용/로깅하는 dispatcher
(`Gate`) 를 제공합니다. Scheduler / Publisher / Worker 등 여러 진입점이 동일 인스턴스를 공유하여
차단 정책을 일관 적용합니다 (이슈 #119).

<br>

## API

[guard.go](../../../pkg/urlguard/guard.go), [gate.go](../../../pkg/urlguard/gate.go), [pattern.go](../../../pkg/urlguard/pattern.go):

```go
type Guard interface {
    Allow(url string) (allowed bool, reason string)
}

type AllowAllGuard struct{}  // no-op (테스트 / 비활성화)

type Gate struct { /* … */ }

func NewGate(guard Guard, log *logger.Logger) *Gate
func (g *Gate) Allow(url string) bool         // 차단 시 WARN 로그 자동
func (g *Gate) Filter(urls []string) []string // 일괄 필터 + 사유 로그
```

<br>

## 사용 패턴

```go
gate := urlguard.NewGate(urlguard.Default(), log)

// 각 진입점이 동일 gate 인스턴스 공유
scheduler.SetGate(gate)
publisher.SetGate(gate)
pool.SetGate(gate)
```

각 진입점은 publish/dispatch 직전에 `gate.Allow(url)` 호출. 차단 시 silent drop + WARN.

<br>

## 의존

- [`pkg/logger`](logger.md) — 차단 사유 로깅
- 본 패키지가 import 하는 다른 패키지: 없음 (sink 노드)

<br>

## 호출 측

- [`internal/scheduler`](../internal/scheduler.md) — seed publish 직전
- [`internal/publisher`](../internal/publisher.md) — chained job publish 직전
- [`internal/processor/fetcher/worker`](../internal/processor/fetcher/worker.md) — 추가 가드 적용 가능

<br>

## 관련 이슈

- 이슈 #119 — URL Guard 도입
