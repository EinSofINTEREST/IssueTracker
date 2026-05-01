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

| 메트릭                              | 종류       | 위치                                                                | 의미                                |
|------------------------------------|-----------|---------------------------------------------------------------------|-------------------------------------|
| Go runtime / process               | Collector | NewRegistry 자동                                                     | GC, goroutine, mem, fd 등           |
| `refiner_attempts`                 | Counter   | [refiner/metrics.go](../../../internal/processor/parser/rule/refiner/metrics.go) | path_pattern 정밀화 시도 횟수 (PR #191) |
| (그 외) Circuit Breaker 상태 등    | (개별 패키지가 자체 등록) | -                                                  |                                     |

신규 메트릭 추가 시 해당 패키지 (예: `internal/processor/parser/rule/refiner`) 가
`prometheus.NewCounterVec` 등으로 만들고 `metricsRegistry.MustRegister` 로 등록 — registry 인스턴스를
[`cmd/issuetracker`](../cmd/issuetracker.md) 가 wiring 시점에 주입.

<br>

## 의존

- 외부: `github.com/prometheus/client_golang/prometheus`, `prometheus/promhttp`
- [`pkg/logger`](logger.md)

<br>

## Wiring 위치

- [`cmd/issuetracker`](../cmd/issuetracker.md) 단계 1
- [`cmd/processor`](../cmd/processor.md) 동일

<br>

## 관련 이슈

- 이슈 #165 — metrics endpoint 도입
- PR #191 — refiner_attempts counter 추가
