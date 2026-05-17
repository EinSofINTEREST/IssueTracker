# internal/processor/parser — Domain-Agnostic Parser + Claim Check Worker

소스: [`internal/processor/parser/`](../../../../../internal/processor/parser/)

본 패키지는 두 layer 로 구성됩니다 (이슈 #196 통합):

1. **Rule engine** ([rule.md](rule.md)) — `types/types.go` (도메인 중립 인터페이스) + `rule/` (DB-driven engine, indexonly heuristic, auto_demote, llmgen, pathinfer, refiner, blacklist matcher)
2. **Kafka worker** ([worker/](../../../../../internal/processor/parser/worker/)) — Claim Check 패턴으로 raw 로드 + rule engine 호출 + content 저장

이슈 #134 에서 fetcher 와 parser 를 분리하면서 도입된 별도 consumer group `issuetracker-parsers`.
[Claim Check 패턴](https://learn.microsoft.com/en-us/azure/architecture/patterns/claim-check) 으로
Kafka 메시지에 raw HTML 을 싣지 않고 DB 에서 로드합니다.

<br>

## 구성

| 파일 | 역할 |
|------|------|
| [worker.go](../../../../../internal/processor/parser/worker/worker.go) | `Worker` — TopicFetched consume + parse + content store (이슈 #419 으로 `parser_worker.go` → `worker.go`, `ParserWorker` → `Worker` rename) |
| [cleanup.go](../../../../../internal/processor/parser/worker/cleanup.go) | `RawContentCleaner` — cron 으로 오래된 raw_contents 정리 |

<br>

## 동작 흐름 (per message)

```
1. Kafka.FetchMessage (TopicFetched)
   → ProcessingMessage → RawContentRef
2. RawContentService.GetByID(ref.ID) → core.RawContent (HTML)
3. ProcessingLock.Acquire("parser", raw.URL)
4. target_type 분기:
   ┌─ TargetTypeList (category / index)
   │  ├ rule.Parser.ParseLinks(ctx, raw) → []LinkItem
   │  └ Publisher.PublishBatch(...) — chained article CrawlJob 발행
   └─ TargetTypePage (article)
      ├ rule.Parser.ParsePage(ctx, raw) → Page
      │     ├ ErrNoRule → llmGen.Enqueue (이슈 #149) + commit (raw 잔존)
      │     ├ ErrParseFailure / ErrEmptySelector → commit (raw 잔존, LLM 재처리 대기)
      │     └ 성공 + index-only 휴리스틱 통과 시 (이슈 #477):
      │           autoDemoter.demoteAsync (비동기) →
      │           parser_blacklist 에 mode='extract_links_only' 자동 등록 →
      │           다음 호출부터 BlacklistMatcher 가 list 모드로 라우팅
      ├ general.ConvertPageToContent(page) → core.Content (Article 플래그 전파, 이슈 #423)
      ├ ContentService.Store → ContentRef
      ├ Producer.Publish(TopicNormalized, ContentRef)
      └ Resolver 매칭 결과 + sample URL 누적 (SampleURLRepository.Insert) — 이슈 #173 단계 4-1
5. RawContentService.Delete(ref.ID) — Claim Check 정리
6. Kafka.CommitMessages
```

**실패 정책**:
- `rule.Error` 계열: raw 잔존 + commit (재시도 X) — LLM 재처리 대기
- 기타 transient 에러: commit 안 함 → Kafka 재배달

**graceful shutdown**: `Worker.Stop()` 가 `Parser.WaitAutoDemote()` 도 함께 호출 — in-flight async demote goroutine 완료까지 대기.

<br>

## RawContentCleaner

Worker 가 이상 종료 / rule.Error 잔존 / LLM 재처리 윈도우 만료된 row 가 누적되는 것을 방지하는
간단한 cron. [`cleanup.go`](../../../../../internal/processor/parser/worker/cleanup.go) 가 일정 주기마다
`RawContentService.PurgeOlderThan(...)` 호출.

<br>

## 의존

- [`internal/processor/parser/rule`](rule.md) — `Parser`, `Resolver`, `BlacklistMatcher`
- [`internal/processor/parser/rule/llmgen`](rule.md#4-llm-selector-generator) — `Generator.Enqueue` (선택)
- [`internal/processor/parser/rule/indexonly`](rule.md#3-index-only-heuristic) — `IsIndexOnly` (auto-demote 흐름 내부)
- [`internal/processor/fetcher/domain/general`](../fetcher/domain.md) — `ConvertPageToContent`
- [`internal/locks`](../../locks/README.md) — `ProcessingLock`
- [`internal/storage/service`](../../storage/service.md) — `RawContentService`, `ContentService`, `BlacklistService` (auto-demote 의존성 역전)
- [`internal/storage`](../../storage/README.md) — `SampleURLRepository`
- [`internal/publisher`](../../publisher.md) — chained job 발행 (BlacklistMatcher.Classify 사후 분류)
- [`internal/processor/precheck`](../precheck.md) — 발행 직전 URL 처리 가부 게이트 (이슈 #425)
- [`pkg/agent/claude`](../../../pkg/agent/claude.md) — EnrichedExtractor (llmgen 의 LLM 호출 백엔드)
- [`pkg/queue`](../../../pkg/queue.md), [`pkg/logger`](../../../pkg/logger.md)

<br>

## Wiring 위치

[`cmd/issuetracker/main.go`](../../../../../cmd/issuetracker/main.go) — parser worker 단계, cleanup cron 단계.

`rule.Parser` 생성 시 `WithBlacklistAutoDemote(blacklistSvc, metrics, log)` 옵션 주입 (이슈 #477) —
`BLACKLIST_ENABLED=false` 환경에서는 옵션이 noop 으로 기존 동작 유지.

자세한 wiring 은 [cmd/issuetracker.md](../../../cmd/issuetracker.md) 참조.

<br>

## 외부 시스템

- Kafka: `issuetracker.fetched` consume / `issuetracker.normalized` + `issuetracker.crawl.{high,normal,low}` (chained jobs) produce
- PostgreSQL:
  - `raw_contents` (load+delete)
  - `contents` (store)
  - `parser_rule_sample_urls` (insert)
  - `parser_blacklist` (auto_demote INSERT — async, mode='extract_links_only')
- Redis: ProcessingLock (단계="parser")

<br>

## 관련 이슈

- 이슈 #134 — fetcher / parser 분리, Claim Check
- 이슈 #149 — LLM 기반 selector 자동 생성
- 이슈 #173 단계 4-1 — sample URL 누적
- 이슈 #178 — ProcessingLock 단계 prefix
- 이슈 #196 — Rule engine + Worker 통합 패키지화
- 이슈 #419 — `parser_worker.go` → `worker.go` + `ParserWorker` → `Worker` rename
- 이슈 #423 — `parser_rules.article` 다운스트림 전파
- 이슈 #425 — precheck 패키지 신설 + 진입점 게이트
- 이슈 #463 — RuleLookup interface + extract.go 분리
- 이슈 #477 — ParsePage 결과 index-only 자동 강등 wiring
- 이슈 #480 — LLM auto-blacklist mode 분기
- 이슈 #482 — 부팅 시 `VerifySeeded` 폐기 (DB-driven readiness 신뢰)
