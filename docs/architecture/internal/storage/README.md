# internal/storage — Layered Data Access (model / repository / primitive / decorator / service / postgres / redis)

소스: [`internal/storage/`](../../../../internal/storage/)

이슈 #430/#431 (Phase 1/2 layering) 으로 도메인별 sub-package 로 명확히 분리. 데이터 모델 (model) /
인터페이스 (repository / primitive) / cross-cutting decorator / 비즈니스 로직 (service) / SQL 구현 (postgres) / Redis 구현이
각각 자기 책임만 가짐.

<br>

## 레이어 분리

```
┌─────────────────────────────────────────────────────────┐
│  Service Layer  (internal/storage/service/)             │
│  ContentService / RawContentService /                   │
│  BlacklistService / ParserRuleService                   │
│  - 비즈니스 로직 (중복 감지, LLM 결정 처리, ...)        │
│  - Decorator chain 자동 합성 (timeout + invalidator)    │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────┐
│  Decorator Layer  (internal/storage/decorator/)         │
│  WrapBlacklistWithTimeout / WrapBlacklistWithInvalidator│
│  WrapParserRuleWithTimeout / WrapWithInvalidator        │
│  WrapContentWithTimeout / WrapRawContentWithTimeout     │
│  WrapFetcherRuleWithTimeout                             │
│  - cross-cutting (query timeout / cache invalidate)     │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────┐
│  Repository Layer  (internal/storage/repository/)       │
│  ContentRepository / RawContentRepository /             │
│  ParserRuleRepository / BlacklistRepository /           │
│  FetcherRuleRepository / SampleURLRepository /          │
│  SchedulerEntryRepository / EnrichedContentRepository / │
│  SearchKeywordRepository                                │
│  - 순수 CRUD 인터페이스만 (이슈 #430)                   │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────┐
│  Primitive Layer  (internal/storage/primitive/)         │
│  InflightLocker — (host, targetType) 단위 lock 추상     │
│  - Mem (in-process) / Redis 구현 두 종                  │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
┌──────────────────────────┬──────────────────────────────┐
│  PostgreSQL              │  Redis                       │
│  internal/storage/postgres/ │  internal/storage/redis/  │
│  pgx/v5 + Wrap*WithTimeout │  goredis client + Lua       │
│  레이터 (이슈 #427)       │  스크립트 (atomic release) │
└──────────────────────────┴──────────────────────────────┘
```

<br>

## Sub-package

| Sub-package | 책임 | 주요 타입 |
|---|---|---|
| [`model/`](../../../../internal/storage/model/) | 도메인 모델 + 공유 타입 | `BlacklistRecord`, `BlacklistMode`, `ParserRuleRecord`, `SelectorMap`, `TargetType`, `ValidationStatus*` 상수 (`Pending`/`Passed`/`Rejected`), `ContentFilter`, `RawContentFilter`, `BlacklistFilter`, `ParserRuleFilter` |
| [`repository/`](../../../../internal/storage/repository/) | Repository 인터페이스 (순수 CRUD) | `ContentRepository`, `RawContentRepository`, `BlacklistRepository`, `ParserRuleRepository`, `FetcherRuleRepository`, `SampleURLRepository`, `SchedulerEntryRepository`, `EnrichedContentRepository`, `SearchKeywordRepository` |
| [`primitive/`](../../../../internal/storage/primitive/) | 인프라 primitives (lock 등) | `InflightLocker` interface + Mem 구현 |
| [`decorator/`](../../../../internal/storage/decorator/) | Cross-cutting (timeout / cache invalidate) | `WrapBlacklistWithTimeout`, `WrapBlacklistWithInvalidator`, `WrapWithInvalidator` (parser_rule), `WrapContentWithTimeout`, `WrapRawContentWithTimeout`, `WrapFetcherRuleWithTimeout` |
| [`service/`](../../../../internal/storage/service/) | 비즈니스 로직 (boundary) — [service.md](service.md) 참조 | `ContentService`, `RawContentService`, `BlacklistService` (이슈 #431, #480), `ParserRuleService` (이슈 #431) |
| [`postgres/`](../../../../internal/storage/postgres/) | PostgreSQL 구현 — [postgres.md](postgres.md) 참조 (query-level timeout 은 `decorator/timeout.go` 가 담당) | `NewBlacklistRepository`, `NewParserRuleRepository`, `NewContentRepository`, ... |
| [`redis/`](../../../../internal/storage/redis/) | Redis 구현 (분산 lock, sliding window) | `NewInflightLocker`, `NewIngestionLocker`, `NewSlidingWindow` 등 |

<br>

## Repository 인터페이스 (이슈 #430 Phase 1)

| 인터페이스 | 위치 | 테이블 |
|---|---|---|
| `ContentRepository` | [repository/content.go](../../../../internal/storage/repository/content.go) | `contents` + `content_bodies` + `content_meta` (3-table 트랜잭션) |
| `RawContentRepository` | [repository/raw_content.go](../../../../internal/storage/repository/raw_content.go) | `raw_contents` (Claim Check, 이슈 #134) |
| `ParserRuleRepository` | [repository/parser_rule.go](../../../../internal/storage/repository/parser_rule.go) | `parser_rules` (이슈 #100, rename: 이슈 #429) |
| `BlacklistRepository` | [repository/blacklist.go](../../../../internal/storage/repository/blacklist.go) | `parser_blacklist` (이슈 #295/#297) |
| `FetcherRuleRepository` | [repository/fetcher_rule.go](../../../../internal/storage/repository/fetcher_rule.go) | `fetcher_rules` (host 단위 fetcher chain, 이슈 #425/#426) |
| `SampleURLRepository` | [repository/sample_url.go](../../../../internal/storage/repository/sample_url.go) | `parser_rule_sample_urls` (이슈 #173 단계 4-1) |
| `SchedulerEntryRepository` | [repository/scheduler_entry.go](../../../../internal/storage/repository/scheduler_entry.go) | `scheduler_entries` (이슈 #124/#243) |
| `EnrichedContentRepository` | [repository/enriched_content.go](../../../../internal/storage/repository/enriched_content.go) | `enriched_contents` (이슈 #450, rationale/factors 컬럼: 이슈 #457) |
| `SearchKeywordRepository` | [repository/search_keyword.go](../../../../internal/storage/repository/search_keyword.go) | `search_keywords` (이슈 #244) |

<br>

## Decorator chain (이슈 #427 / #431)

운영 wiring 에서 repository 인스턴스를 **timeout** + **invalidator** 데코레이터로 감싸 사용:

```
raw repo → invalidator (cache 무효화) → timeout (query-level deadline) → 최종 사용처
```

[`decorator/timeout.go`](../../../../internal/storage/decorator/timeout.go) 가 각 타입별 `Wrap*WithTimeout` 제공.
[`decorator/blacklist_invalidating.go`](../../../../internal/storage/decorator/blacklist_invalidating.go) / [`decorator/parser_rule_invalidating.go`](../../../../internal/storage/decorator/parser_rule_invalidating.go) 가
mutation 후 cache invalidate (Matcher / Resolver 의 cache 항목 무효화).

`service.NewBlacklistService` / `service.NewParserRuleService` 가 본 chain 을 **자동 합성** —
wiring 측은 raw repo + invalidator 객체만 전달:

```go
blacklistSvc := service.NewBlacklistService(
    blacklistRepoRaw, log,
    service.WithBlacklistQueryTimeout(dbCfg.QueryTimeout),
    service.WithBlacklistInvalidator(blacklistMatcher),
)
```

<br>

## Query-level timeout (이슈 #427)

pgxpool 의 `Acquire()` 가 풀 고갈 시 **무한 블로킹** 되는 문제 — [`decorator/timeout.go`](../../../../internal/storage/decorator/timeout.go) 의 `Wrap*WithTimeout` 데코레이터들이 각 Repository 메소드 진입에 query-level timeout 을 적용 (`WrapBlacklistWithTimeout` / `WrapParserRuleWithTimeout` / `WrapContentWithTimeout` / `WrapRawContentWithTimeout` / `WrapFetcherRuleWithTimeout` / `WrapEnrichedContentWithTimeout` 등). [`cmd/issuetracker/main.go`](../../../../cmd/issuetracker/main.go) 가 `storage.Load()` 결과의 `QueryTimeout` 으로 wrap 한 repository 를 service 에 전달.

<br>

## 에러 / 필터

| 파일 | 책임 |
|---|---|
| [errors.go](../../../../internal/storage/errors.go) | `ErrNotFound`, `ErrDuplicate`, `ErrInvalid` 등 sentinel 에러 |
| [`model/`](../../../../internal/storage/model/) | `*Filter` 타입 — limit/offset/where 조건 빌더 (도메인별 파일에 분산: `blacklist.go`, `parser_rule.go`, 등) |

<br>

## 의존

- [`internal/processor/fetcher/core`](../processor/fetcher/core.md) — `Content`, `RawContent`
- [`pkg/config/storage`](../../pkg/config.md) — `Load` (DB), `LoadRedis`, `IngestionLockTTL`
- [`pkg/logger`](../../pkg/logger.md)
- 외부: `github.com/jackc/pgx/v5`, `github.com/jackc/pgerrcode`, `github.com/redis/go-redis/v9`

<br>

## 관련 이슈

- 이슈 #100 — DB-driven parser rules
- 이슈 #124 — scheduler_entries
- 이슈 #134 — Claim Check (raw_contents 분리)
- 이슈 #135 / #161 — validation_status 영속화
- 이슈 #173 — sample_urls 누적 / refiner
- 이슈 #243 — scheduler_entries 운영 확장
- 이슈 #244 — search_keywords
- 이슈 #295 / #297 — parser_blacklist (drop / extract_links_only)
- 이슈 #326 — LLM auto-blacklist 등록 (BlacklistService.HandleLLMDecision 의 전신)
- 이슈 #421 — parser_rules.article 컬럼
- 이슈 #425 / #426 — fetcher_rules
- 이슈 #427 — query-level timeout 데코레이터 (`Wrap*WithTimeout`) — pgxpool Acquire 무한 블로킹 차단
- 이슈 #429 — `parsing_rules` → `parser_rules` rename
- **이슈 #430 — Phase 1 layering** (model / repository / primitive / decorator 분리)
- **이슈 #431 — Phase 2 service layer** (BlacklistService + ParserRuleService + decorator chain 자동 합성)
- 이슈 #432 — storage 도메인 audit (Phase 3 — 단순 CRUD service 회의적, 직접 호출 정책)
- 이슈 #450 — enriched_contents
- 이슈 #457 — enriched_contents.rationale + factors 컬럼화
- 이슈 #480 — `HandleLLMDecision` mode 인자 추가
