# pkg/metrics — Prometheus Registry & HTTP Endpoint

소스: [`pkg/metrics/metrics.go`](../../../pkg/metrics/metrics.go)

Prometheus `Registry` 생성과 `/metrics` HTTP endpoint 노출을 담당하는 얇은 helper. 모든 컴포넌트가
동일 registry 를 공유하여 단일 endpoint 로 export.

<br>

## API

```go
metricsRegistry := metrics.NewRegistry()  // Go runtime + process collectors 포함

stop, err := metrics.Serve(ctx, ":9090", metricsRegistry, log)
defer stop()
```

`Serve` 는 nonblocking — context cancel 또는 stop() 호출까지 background goroutine 으로 동작.

`addr` 가 빈 문자열이면 endpoint 미기동 (`METRICS_ADDR` ENV 가 빈 값일 때).

<br>

## 등록되는 메트릭

각 모듈이 자체적으로 `NewCounterVec` 등을 만들고 본 registry 에 `MustRegister` 또는 idempotent register helper 로 등록.

| 메트릭 | 종류 | 위치 | 의미 | Label |
|---|---|---|---|---|
| Go runtime / process | Collector | `NewRegistry` 자동 | GC, goroutine, mem, fd 등 | — |
| `refinement_attempts` | Counter | [`rule/refiner/metrics.go`](../../../internal/processor/parser/rule/refiner/metrics.go) | path_pattern 정밀화 시도 (PR #191) | `result` (success/skipped/error), `method` (algorithm/llm/none) |
| `refinement_llm_calls_total` | Counter | [`rule/refiner/metrics.go`](../../../internal/processor/parser/rule/refiner/metrics.go) | refiner 의 LLM 호출 결과 | `status` (success/error) |
| `parser_index_page_auto_demoted_total` | Counter | [`rule/auto_demote_metrics.go`](../../../internal/processor/parser/rule/auto_demote_metrics.go) | index-only 페이지 자동 강등 (이슈 #477) | `host` |
| `llm_*` (pkg/llm 자체) | Histogram / Counter | [`pkg/llm/measured.go`](../../../pkg/llm/measured.go) | provider 별 latency / 호출 결과 | provider, status 등 |
| (그 외) Circuit Breaker / Worker Pool 등 | 개별 패키지 자체 등록 | — | — | — |

신규 메트릭 추가 시 해당 패키지 (예: `internal/processor/parser/rule/auto_demote_metrics.go`) 가
`prometheus.NewCounterVec` 등으로 만들고 호출자가 registry 인스턴스를 전달해서 등록. registry 인스턴스를
[`cmd/issuetracker`](../cmd/issuetracker.md) 가 wiring 시점에 주입.

<br>

## 등록 패턴 — idempotent register

여러 위치에서 동일 collector 가 등록 시도되더라도 panic 없이 기존 collector 재사용. [`rule/auto_demote_metrics.go`](../../../internal/processor/parser/rule/auto_demote_metrics.go) 의 `registerOrReuseAutoDemoteCounter` / [`rule/refiner/metrics.go`](../../../internal/processor/parser/rule/refiner/metrics.go) 의 `registerOrReuseCounter` / [`pkg/llm/measured.go`](../../../pkg/llm/measured.go) 의 `MeasuredFactory` 가 동일 패턴 적용:

```go
counter := prometheus.NewCounterVec(opts, labels)
if err := registry.Register(counter); err != nil {
    var are prometheus.AlreadyRegisteredError
    if errors.As(err, &are) {
        if existing, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
            return existing  // 재사용
        }
    }
    panic(err)  // incompatible collector 충돌
}
```

<br>

## nil-safe Metrics 패턴

`*Metrics` 가 nil 이거나 내부 collector 가 nil 이면 `Record*` 메소드가 noop — 호출자는 nil 검사 없이 항상 호출 가능. `NewAutoDemoteMetrics(nil)` 또는 `NewAutoDemoteMetrics(registry)` 둘 다 허용 — METRICS 비활성 환경 cover:

```go
func (m *AutoDemoteMetrics) RecordAutoDemote(host string) {
    if m == nil || m.autoDemoted == nil {
        return
    }
    m.autoDemoted.WithLabelValues(host).Inc()
}
```

<br>

## 의존

- 외부: `github.com/prometheus/client_golang/prometheus`, `prometheus/promhttp`
- [`pkg/logger`](logger.md)

<br>

## Wiring 위치

- [`cmd/issuetracker`](../cmd/issuetracker.md) 단계 1 — registry 생성 + endpoint serve
- [`cmd/processor`](../cmd/processor.md) 동일

각 모듈은 registry 를 wiring 시점에 받아 자기 collector 를 등록:

```go
// cmd/issuetracker/main.go
autoDemoteMetrics := rule.NewAutoDemoteMetrics(metricsRegistry)
parserOpts = append(parserOpts, rule.WithBlacklistAutoDemote(blacklistSvc, autoDemoteMetrics, log))
```

<br>

## 관련 이슈

- 이슈 #165 — metrics endpoint 도입
- PR #191 — `refinement_attempts` counter 추가
- 이슈 #477 — `parser_index_page_auto_demoted_total` counter 추가
- 이슈 #472 — enrich MCP postgres tool (별도 metric 없음, 추후 enrich 단계별 metric 추가 백로그)
