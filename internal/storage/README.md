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

## 추가 / 변경 시 규칙

1. **새 Record / Filter / Enum** 추가 → `model/` 에 파일 추가
2. **새 Repository 인터페이스** 추가 → `repository/` 에 파일 추가 + postgres 구현
3. **새 cross-cutting decorator** → `decorator/` 에 파일 추가 (interface signature 변경 없이 합성)
4. **새 service** → 비즈니스 가치 (cross-cutting 또는 dedup / status 갱신 등) 가 있을 때만 추가 — thin pass-through 회피
5. **`storage` 패키지 root 에 새 파일 추가 금지** — errors.go 외에는 sub-package 로 분류

## 관련 이슈

- 메타: #429 (storage 패키지 layering 정리)
- Phase 1 (본 README 의 구조 분리): #430
- Phase 2 (BlacklistService + ParserRuleService): #431
- Phase 3 (단순 CRUD service 검토): #432
