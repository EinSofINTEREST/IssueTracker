# internal/processor/fetcher/domain — Generic Fetcher Chain & Source Configs

소스: [`internal/processor/fetcher/domain/`](../../../../internal/processor/fetcher/domain/)
주 패키지: [`internal/processor/fetcher/domain/general/`](../../../../internal/processor/fetcher/domain/general/)

도메인 중립 (news / community / blog 모두 공용) 의 Fetcher Chain of Responsibility 와, 사이트별 config
(카테고리 URL / RPS / Source 메타) 등록 지점입니다.

<br>

## 핵심 추상

| 위치                                                                      | 역할                                                            |
|--------------------------------------------------------------------------|-----------------------------------------------------------------|
| [types.go](../../../../internal/processor/fetcher/domain/general/types.go)          | `SourceCrawler` (=`core.Crawler` 별칭), `Fetcher`, `JobPublisher` 인터페이스 |
| [handler.go](../../../../internal/processor/fetcher/domain/general/handler.go)      | `FetchHandler` (Chain link) — 실패 시 다음 link 로 위임          |
| [chain_handler.go](../../../../internal/processor/fetcher/domain/general/chain_handler.go) | `ChainHandler` — fetch chain 실행 → raw_contents 저장 → RawContentRef 발행 |
| [source_crawler.go](../../../../internal/processor/fetcher/domain/general/source_crawler.go) | `genericCrawler` — `core.Crawler` 의 default 구현               |
| [convert.go](../../../../internal/processor/fetcher/domain/general/convert.go)      | `parser.Page` ↔ `core.Content` 양방향 변환                      |

<br>

## Fetcher Chain (Chain of Responsibility)

```
ChainHandler (handler.Handler 구현)
    │
    ▼ chain[0]: GoQueryFetchHandler  (정적 HTML 우선 — 빠르고 가벼움)
    │       │
    │       ├ 성공 → RawContent 반환
    │       └ 실패 (lazy 감지 / status 4xx-5xx 일부) → 다음 link
    │
    ▼ chain[1]: BrowserFetchHandler  (chromedp 헤드리스 Chrome)
            │
            ├ 성공 → RawContent
            └ 실패 → CrawlerError 반환 (worker 가 retry/CB 처리)
```

각 chain link 의 구현은 [implementation.md](implementation.md) 참조 (실제 fetcher 는
`fetcher/goquery.go`, `fetcher/browser.go` 가 담당).

<br>

## 사이트별 config

```
domain/general/sources/
├── kr/
│   ├── registry.go      ← kr.Register(registry, cfg, rawSvc, producer, log)
│   ├── naver/config.go  ← Naver 카테고리 URL + RequestsPerHour
│   ├── daum/config.go
│   └── yonhap/config.go
└── us/
    ├── registry.go      ← us.Register(...)
    └── cnn/config.go
```

각 `config.go` 는:

- `SourceInfo` (Country, Type=News, Name, BaseURL, Language)
- 카테고리 URL 목록 (스케줄러 seed 로 사용)
- `RequestsPerHour` (rate_limiter 에 전달)

`registry.go` 는 사이트별 ChainHandler 를 구성하여 [`handler.Registry`](handler.md) 에 등록합니다.

<br>

## 사용 흐름

[`cmd/issuetracker/main.go`](../../../../cmd/issuetracker/main.go):

```go
registry := handler.NewRegistry(log)
kr.Register(registry, core.DefaultConfig(), rawSvc, crawlerProducer, log)
us.Register(registry, core.DefaultConfig(), rawSvc, crawlerProducer, log)
```

`Register` 함수는 사이트별로:
1. `goquery.Crawler` + `chromedp.Crawler` 인스턴스 생성
2. `BuildChain` 으로 Fetcher chain 조립
3. `ChainHandler` 로 wrap (raw_contents 저장 + RawContentRef 발행 책임)
4. `registry.Register("naver", chainHandler)` 등록

<br>

## 의존

- [`internal/processor/fetcher/core`](core.md) — 모든 인터페이스/모델
- [`internal/processor/fetcher/handler`](handler.md) — Registry 등록 대상
- [`internal/processor/fetcher/implementation/{goquery,chromedp}`](implementation.md) — 실제 fetcher
- [`internal/storage/service`](../../storage/service.md) — `RawContentService` (Claim Check 저장)
- [`pkg/queue`](../../pkg/queue.md) — Kafka producer
- [`pkg/links`](../../pkg/links.md) — URL normalize / link extract

<br>

## 새 사이트 추가 절차

1. `sources/<country>/<site>/config.go` 생성 (`SourceInfo` + 카테고리 URL + RPS)
2. `sources/<country>/registry.go` 의 `Register` 에 site 추가
3. [`internal/scheduler.DefaultEntries`](../../scheduler.md) 에 카테고리 seed entry 추가
4. `parsing_rules` 에 host_pattern + path_pattern + selectors 시드 (마이그레이션 또는 운영자 INSERT)
   — `path_pattern` 은 빈 문자열이면 catch-all 로 동작하며, [refiner](../../parser/rule.md) 가 누적 sample
   기반으로 정밀화합니다 (이슈 #173).
