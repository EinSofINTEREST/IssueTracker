# internal/processor/fetcher/handler — Crawler Registry

소스: [`internal/processor/fetcher/handler/`](../../../../internal/processor/fetcher/handler/)

`crawler_name` (예: `"naver"`, `"cnn"`) 을 실제 처리 함수에 매핑하는 **Registry**. [`worker/PoolManager`](worker.md)
가 Kafka 에서 받은 `CrawlJob.CrawlerName` 으로 본 Registry 를 lookup 해서 처리를 위임합니다.

<br>

## 인터페이스 + 구현

| 위치                                                                  | 역할                                                |
|----------------------------------------------------------------------|-----------------------------------------------------|
| [handler.go](../../../../internal/processor/fetcher/handler/handler.go)         | `Handler` 인터페이스 + `Registry` (map + fallback)  |
| [noop.go](../../../../internal/processor/fetcher/handler/noop.go)               | unknown crawler_name 에 대한 fallback (Warn 로그)   |

```go
type Handler interface {
    Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error)
}

type Registry struct {
    handlers map[string]Handler
    fallback Handler
    log      *logger.Logger
}
```

`Registry` 는 mutex 가 없습니다 — 등록은 entry point ([`cmd/issuetracker`](../../cmd/issuetracker.md))
의 wiring 단계에서 단일 goroutine 으로 일괄 수행되고, 그 후 worker 들의 read-only lookup 만 발생하므로
race 가 없습니다 (등록/조회 동시성을 도입한다면 별도 mutex 필요).

<br>

## 사용 흐름

```
1. cmd/issuetracker/main.go 가 NewRegistry(log) 생성
2. kr.Register(...) / us.Register(...) 가 사이트별 Handler 를 등록
3. PoolManager 가 worker 마다 Registry.Handle(ctx, job) 호출
4. CrawlerName 에 매칭 없으면 noop.Handler 가 Warn 로그 + 빈 결과 반환
```

<br>

## 등록 측

`internal/processor/fetcher/domain/general/sources/{kr,us}/registry.go` 가 `Register(registry, …)` 함수를 노출하며,
[`cmd/issuetracker`](../../cmd/issuetracker.md) 에서 호출. 자세한 구조는
[domain.md](domain.md) 참조.

<br>

## 의존

- [`internal/processor/fetcher/core`](core.md) — `CrawlJob`, `Content`
- [`pkg/logger`](../../pkg/logger.md)

<br>

## Test 위치

[`test/internal/processor/fetcher_handler/`](../../../../test/internal/) (있다면) — Registry 등록/조회/충돌 처리 검증.
