# internal/scheduler — Seed Job Scheduler

소스: [`internal/scheduler/`](../../../internal/scheduler/)

등록된 카테고리 URL 목록 (CNN sections / Naver categories / …) 을 **주기적으로 Kafka crawl 토픽에
시드 발행**합니다. 크롤 결과로부터 발견된 URL 의 chained 발행은 [publisher](publisher.md) 책임.

<br>

## 구성

| 파일                                                          | 역할                                                  |
|--------------------------------------------------------------|-------------------------------------------------------|
| [scheduler.go](../../../internal/scheduler/scheduler.go)      | `Scheduler` — entry 별 polling goroutine 관리         |
| [entries.go](../../../internal/scheduler/entries.go)          | `DefaultEntries(SchedulerConfig)` — CNN/Naver/Yonhap/Daum entries  |
| [emitter.go](../../../internal/scheduler/emitter.go)          | `JobEmitter` — Kafka 발행 어댑터                      |
| [source.go](../../../internal/scheduler/source.go)            | source 별 entry 빌더 helper                            |
| [throttle.go](../../../internal/scheduler/throttle.go)        | `BacklogThrottler` — consumer-group lag 임계값 검사  |

<br>

## 핵심 타입

```go
type ScheduleEntry struct {
    CrawlerName string
    URL         string
    TargetType  core.TargetType
    Interval    time.Duration
    Priority    core.Priority
    Timeout     time.Duration
}

type Throttler interface {
    ShouldThrottle(ctx context.Context, job *core.CrawlJob) bool
}

type JobEmitter interface {
    Emit(ctx, job core.CrawlJob) error
}
```

<br>

## 동작

```
Start(ctx):
   for each entry:
      go func() {
         tick := time.NewTicker(entry.Interval)
         for range tick.C:
            if Gate.Allow(entry.URL) == false → drop
            if Throttler.ShouldThrottle() == true → drop (이슈 #124)
            job := buildJob(entry)
            Emitter.Emit(ctx, job)
      }
```

옵션 의존성 (Gate, Throttler) 은 atomic.Pointer 로 lock-free 하게 주입/조회.

`ShouldThrottle` 이 `error` 를 반환하지 않는 이유: throttle 결정은 best-effort 운영 신호이며 실패 시 통과
정책 (false 반환) 으로 graceful degrade — 구현체 (`BacklogThrottler`) 가 내부에서 WARN 로그를 책임집니다.

<br>

## BacklogThrottler

이슈 #124. crawl 토픽의 consumer-group lag 가 `MaxBacklog` 초과면 publish 차단:

```go
backlogChecker := queue.NewBacklogChecker(brokers, timeout)
throttler := scheduler.NewBacklogThrottler(
    backlogChecker, queue.GroupCrawlerWorkers, MaxBacklog, timeout, log,
)
sched.SetThrottler(throttler)
```

`SCHEDULER_MAX_BACKLOG` 환경변수가 0 이면 비활성.

<br>

## 의존

- [`internal/crawler/core`](crawler/core.md) — `CrawlJob`, `Priority`, `TargetType`
- [`internal/crawler/domain/general/sources/{kr,us}/.../config`](crawler/domain.md) — 카테고리 URL
- [`pkg/queue`](../pkg/queue.md) — Kafka producer / BacklogChecker
- [`pkg/urlguard`](../pkg/urlguard.md), [`pkg/logger`](../pkg/logger.md)

<br>

## Wiring 위치

[`cmd/issuetracker/main.go`](../../../cmd/issuetracker/main.go) 단계 13:
```go
schedulerCfg, _ := config.LoadScheduler()
emitter := scheduler.NewJobEmitter(crawlerProducer, log)
entries := scheduler.DefaultEntries(schedulerCfg)
sched := scheduler.New(entries, emitter, log, schedulerCfg.MaxRetries)
if schedulerCfg.MaxBacklog > 0 {
    sched.SetThrottler(...)
}
sched.Start(ctx)
```

<br>

## 외부 시스템

- Kafka: `issuetracker.crawl.{high,normal,low}` produce + (BacklogChecker 가) `__consumer_offsets` 조회

<br>

## 관련 이슈

- 이슈 #119 — URL Guard
- 이슈 #124 — BacklogThrottler
- 이슈 #126 — Scheduler / Publisher 책임 분리
