# internal/parser — Claim Check Parser Worker

소스: [`internal/parser/worker/`](../../../../internal/parser/worker/)

> ⚠️ 이름 주의: 본 패키지(`internal/parser`)는 [internal/crawler/parser](../crawler/parser.md) 와 다릅니다.
> `internal/crawler/parser` 가 **rule engine** 이라면, 본 패키지는 그 engine 을 사용하는 **Kafka worker** 입니다.

이슈 #134 에서 fetcher 와 parser 를 분리하면서 도입된 별도 consumer group `issuetracker-parsers`.
[Claim Check 패턴](https://learn.microsoft.com/en-us/azure/architecture/patterns/claim-check) 으로
Kafka 메시지에 raw HTML 을 싣지 않고 DB 에서 로드합니다.

<br>

## 구성

| 파일                                                                    | 역할                                                |
|------------------------------------------------------------------------|-----------------------------------------------------|
| [parser_worker.go](../../../../internal/parser/worker/parser_worker.go) | `ParserWorker` — TopicFetched consume + parse + content store |
| [cleanup.go](../../../../internal/parser/worker/cleanup.go)             | `RawContentCleaner` — cron 으로 오래된 raw_contents 정리 |

<br>

## 동작 흐름 (per message)

```
1. Kafka.FetchMessage (TopicFetched)
   → ProcessingMessage → RawContentRef
2. RawContentService.GetByID(ref.ID) → core.RawContent (HTML)
3. ProcessingLock.Acquire("parser", raw.URL)
4. target_type 분기:
   ┌─ TargetTypeCategory (list)
   │  ├ rule.Parser.ParseLinks(ctx, raw) → []LinkItem
   │  └ Publisher.PublishBatch(...) — chained article CrawlJob 발행
   └─ TargetTypePage (article)
      ├ rule.Parser.ParsePage(ctx, raw) → Page
      │     ├ ErrNoRule → llmGen.Enqueue (이슈 #149) + commit (raw 잔존)
      │     └ ErrParseFailure / ErrEmptySelector → commit (raw 잔존)
      ├ general.ConvertPageToContent(page) → core.Content
      ├ ContentService.Store → ContentRef
      ├ Producer.Publish(TopicNormalized, ContentRef)
      └ Resolver 매칭 결과 + sample URL 누적 (SampleURLRepository.Insert) — 이슈 #173 단계 4-1
5. RawContentService.Delete(ref.ID) — Claim Check 정리
6. Kafka.CommitMessages
```

**실패 정책**:
- `rule.Error` 계열: raw 잔존 + commit (재시도 X) — LLM 재처리 대기
- 기타 transient 에러: commit 안 함 → Kafka 재배달

<br>

## RawContentCleaner

ParserWorker 가 이상 종료 / rule.Error 잔존 / LLM 재처리 윈도우 만료된 row 가 누적되는 것을 방지하는
간단한 cron. [`cleanup.go`](../../../../internal/parser/worker/cleanup.go) 가 일정 주기마다
`RawContentService.PurgeOlderThan(...)` 호출.

<br>

## 의존

- [`internal/crawler/parser/rule`](../crawler/parser.md) — `Parser`, `Resolver`
- [`internal/crawler/parser/rule/llmgen`](../crawler/parser.md) — `Generator.Enqueue` (선택)
- [`internal/crawler/domain/general`](../crawler/domain.md) — `ConvertPageToContent`
- [`internal/crawler/worker`](../crawler/worker.md) — `ProcessingLock`
- [`internal/storage/service`](../storage/service.md) — `RawContentService`, `ContentService`
- [`internal/storage`](../storage/README.md) — `SampleURLRepository`
- [`internal/publisher`](../publisher.md) — chained job 발행
- [`pkg/queue`](../../pkg/queue.md), [`pkg/logger`](../../pkg/logger.md)

<br>

## Wiring 위치

[`cmd/issuetracker/main.go`](../../../../cmd/issuetracker/main.go) — 단계 10 (parser worker), 단계 12
(cleanup cron). 자세한 wiring 은 [cmd/issuetracker.md](../../cmd/issuetracker.md) 참조.

<br>

## 외부 시스템

- Kafka: `issuetracker.fetched` consume / `issuetracker.normalized` + `issuetracker.crawl.{high,normal,low}` (chained jobs) produce
- PostgreSQL: `raw_contents` (load+delete), `contents` (store), `parsing_rule_sample_urls` (insert)
- Redis: ProcessingLock (단계="parser")

<br>

## 관련 이슈

- 이슈 #134 — fetcher / parser 분리, Claim Check
- 이슈 #149 — LLM 기반 selector 자동 생성
- 이슈 #173 단계 4-1 — sample URL 누적
- 이슈 #178 — ProcessingLock 단계 prefix
