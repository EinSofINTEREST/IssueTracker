# internal/storage

Storage 계층의 layering 가이드. 각 sub-package 는 단일 역할만 책임지며 한 방향으로만 의존합니다.

```
                    ┌──────────────────┐
                    │  Application     │
                    │  (cmd, internal/processor, ...)
                    └────────┬─────────┘
                             │
                             ▼
                    ┌──────────────────┐
                    │     service      │   ← 권장 진입점 (boundary)
                    └────────┬─────────┘
                             │
              ┌──────────────┼──────────────┐
              ▼              ▼              ▼
       ┌──────────┐  ┌──────────────┐  ┌───────────┐
       │ decorator│  │  repository  │  │ primitive │
       └────┬─────┘  └──────┬───────┘  └─────┬─────┘
            │               │                │
            └───────┬───────┴────────┬───────┘
                    ▼                ▼
              ┌──────────┐     ┌──────────┐
              │  model   │     │  errors  │
              └──────────┘     │ (root)   │
                               └──────────┘

       ┌──────────┐     ┌──────────┐
       │ postgres │     │  redis   │   ← 구현체
       └──────────┘     └──────────┘
```

## 패키지 역할

| 패키지 | 역할 | 의존성 |
|---|---|---|
| `model/` | Domain types — Record, Filter, Enum, 보조 struct. **동작 없음**. | (std) |
| `repository/` | DB-backed Repository **인터페이스 정의**만. 구현은 `postgres/`. | `model/` |
| `primitive/` | Redis-backed 단순 키-값 / 카운터 / 큐 primitive **인터페이스 정의** + noop 구현. 구현은 `redis/`. | `model/` (TargetType 등 일부) |
| `decorator/` | Cross-cutting decorator (timeout, invalidating cache). Repository 인터페이스를 받아 동일 인터페이스 반환. | `repository/`, `model/`, `storage` (errors) |
| `service/` | 비즈니스 boundary — 호출자가 우선 사용해야 할 진입점. cross-cutting 로직 (dedup, validation status 갱신 등) 캡슐화. | `repository/`, `model/` |
| `postgres/` | PostgreSQL 구현체. `pgxpool.Pool` 사용. | `repository/`, `model/` |
| `redis/` | Redis 구현체. | `primitive/`, `model/` |
| `storage` (root) | 공통 sentinel 에러 (`ErrNotFound`, `ErrDuplicate`, `ErrInvalid`) + `IsQueryTimeout` helper. 다른 sub-package 에 의존하지 않음. | (std) |

## 호출자 가이드

### 일반 호출자

가능하면 **`service/`** 만 import. 비즈니스 로직 + cross-cutting 모두 service 가 캡슐화.

```go
import "issuetracker/internal/storage/service"

contentSvc := service.NewContentService(...)
contentSvc.Store(ctx, content)
```

### 직접 Repository 사용 시

CRUD 단순 케이스에서 service 가 thin pass-through 만 추가하는 경우는 직접 호출 허용 — Phase 3 (#432) 의 audit 후 의도적 선택.

```go
import (
    "issuetracker/internal/storage/model"
    "issuetracker/internal/storage/repository"
    pgstore "issuetracker/internal/storage/postgres"
)

repo := pgstore.NewSchedulerEntryRepository(pool, log)
entries, err := repo.ListEnabled(ctx, model.SchedulerCategoryNews)
```

### Decorator 합성

main.go 등 wiring 단계에서 repository → decorator chain → service 순으로 합성:

```go
import (
    pgstore "issuetracker/internal/storage/postgres"
    "issuetracker/internal/storage/decorator"
)

base := pgstore.NewContentRepository(pool, log)
withTimeout := decorator.WrapContentWithTimeout(base, dbCfg.QueryTimeout)
// 추가 decorator (e.g., metrics, logging) ...
contentSvc := service.NewContentService(withTimeout, log)
```

## Service vs Repository 직접 호출 정책

`service/` 는 모든 Repository 에 대응되는 layer 가 **아닙니다**. 비즈니스 가치 (cross-cutting 로직 / 묶음 정책 / dedup / 외부 service 통합 등) 가 명확한 도메인만 service 를 추가합니다. 단순 CRUD pass-through 는 호출자가 Repository 를 직접 사용하는 것이 over-engineering 을 회피하는 권장 패턴입니다.

### 현재 정책

| Repository | Service 존재? | 호출자 / 사유 |
|---|---|---|
| `ContentRepository` | ✓ `ContentService` | dedup (ContentHash 검사) / validation status 갱신 / 다중 호출자 (11) |
| `RawContentRepository` | ✓ `RawContentService` | Claim Check 패턴 (저장 + 로드 + 삭제 사이클) |
| `ParserRuleRepository` | ✓ `ParserRuleService` (이슈 #431) | decorator chain 합성 + 11 메서드 / 7 호출자 통합 boundary |
| `BlacklistRepository` | ✓ `BlacklistService` (이슈 #431) | `HandleLLMDecision` 비즈니스 로직 (path_pattern 변환 + ErrDuplicate 흡수) + decorator chain |
| `FetcherRuleRepository` | ✗ (직접 호출) | `fetcher/rule/{Resolver,Upgrader,Downgrader}` + `rate_limiter.SourceConfigResolver` 가 각자 cache + policy 로직 보유 — 이미 도메인 service 역할 (이슈 #432 audit) |
| `SampleURLRepository` | ✗ (직접 호출) | `refiner.Refiner` 가 묶음 정책 (Count + List + Purge) 캡슐화, `parser_worker` 는 단일 Insert (이슈 #432 audit) |
| `SchedulerEntryRepository` | ✗ (직접 호출) | `scheduler.EntryResolver` 가 cache + diff 정책 보유 (이슈 #432 audit) |
| `SearchKeywordRepository` | ✗ (직접 호출) | `search.SearchHandler` 의 단일 `ListEnabled` 호출 (이슈 #432 audit) |

### Repository 직접 호출 시 decorator chain

Service 가 없는 경우 main.go 등 wiring 단계에서 decorator 를 직접 합성합니다:

```go
import (
    pgstore "issuetracker/internal/storage/postgres"
    "issuetracker/internal/storage/decorator"
)

base := pgstore.NewFetcherRuleRepository(pool, log)
fetcherRuleRepo := decorator.WrapFetcherRuleWithTimeout(base, dbCfg.QueryTimeout)
resolver, _ := fetcherRule.NewResolver(fetcherRuleRepo, log, 0)
```

## 추가 / 변경 시 규칙

1. **새 Record / Filter / Enum** 추가 → `model/` 에 파일 추가
2. **새 Repository 인터페이스** 추가 → `repository/` 에 파일 추가 + postgres 구현
3. **새 cross-cutting decorator** → `decorator/` 에 파일 추가 (interface signature 변경 없이 합성)
4. **새 service** → 비즈니스 가치 (cross-cutting 또는 dedup / status 갱신 등) 가 있을 때만 추가 — thin pass-through 회피. 위 "정책 표" 갱신
5. **`storage` 패키지 root 에 새 파일 추가 금지** — errors.go 외에는 sub-package 로 분류

## 관련 이슈

- 메타: #429 (storage 패키지 layering 정리)
- Phase 1 (본 README 의 구조 분리): #430
- Phase 2 (BlacklistService + ParserRuleService): #431
- Phase 3 (단순 CRUD service 검토 audit): #432
