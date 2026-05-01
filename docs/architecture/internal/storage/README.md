# internal/storage — Repository + Service Layer

소스: [`internal/storage/`](../../../../internal/storage/)

데이터 접근 계층의 **인터페이스 + 공유 타입** 을 보유하며, 실제 SQL 구현은 [postgres.md](postgres.md) 가,
비즈니스 로직 (중복 감지, validation status 업데이트 등) 은 [service.md](service.md) 가 담당합니다.

<br>

## 레이어 분리

```
┌─────────────────────────────────────────────────┐
│            Service Layer                        │
│  internal/storage/service/                      │
│  ContentService / RawContentService             │
│  - 중복 감지 (ContentHash / URL)                │
│  - 비즈니스 로직 (validation status update 등)  │
└──────────────────────┬──────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────┐
│            Repository Interfaces                │
│  internal/storage/                              │
│  ContentRepository / RawContentRepository /     │
│  ParsingRuleRepository / SampleURLRepository    │
└──────────────────────┬──────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────┐
│            PostgreSQL Implementation            │
│  internal/storage/postgres/                     │
│  pgx/v5 + pgxpool.Pool 공유                     │
└─────────────────────────────────────────────────┘
```

<br>

## Repository 인터페이스

| 인터페이스                  | 위치                                                              | 테이블                                    |
|----------------------------|------------------------------------------------------------------|-------------------------------------------|
| `ContentRepository`         | [content.go](../../../../internal/storage/content.go)             | `contents` + `content_bodies` + `content_meta` (3-table 트랜잭션) |
| `RawContentRepository`      | [raw_content.go](../../../../internal/storage/raw_content.go)     | `raw_contents` (Claim Check)               |
| `ParsingRuleRepository`     | [parsing_rule.go](../../../../internal/storage/parsing_rule.go)   | `parsing_rules` (이슈 #100)                |
| `SampleURLRepository`       | [sample_url.go](../../../../internal/storage/sample_url.go)       | `parsing_rule_sample_urls` (이슈 #173 4-1) |

공유 타입:
- `TargetType` (page / list)
- `FieldSelector`, `SelectorMap`
- `ParsingRuleRecord` (host_pattern + path_pattern + selectors + source_name + enabled)
- `ContentFilter`, `RawContentFilter` (query 빌더)
- `ValidationStatus` ([validation_status.go](../../../../internal/storage/validation_status.go))

<br>

## 에러 / 필터

| 파일                                                          | 책임                                                      |
|--------------------------------------------------------------|-----------------------------------------------------------|
| [errors.go](../../../../internal/storage/errors.go)           | `ErrNotFound`, `ErrDuplicate` 등 sentinel 에러             |
| [filter.go](../../../../internal/storage/filter.go)           | `ContentFilter`, `RawContentFilter` — limit/offset/where  |

<br>

## 서브패키지

| 디렉토리                                                                          | 문서                                |
|----------------------------------------------------------------------------------|-------------------------------------|
| [`internal/storage/postgres/`](../../../../internal/storage/postgres/)            | [postgres.md](postgres.md)         |
| [`internal/storage/service/`](../../../../internal/storage/service/)              | [service.md](service.md)           |

<br>

## 의존

- [`internal/crawler/core`](../crawler/core.md) — `Content`, `RawContent`
- [`pkg/config`](../../pkg/config.md), [`pkg/logger`](../../pkg/logger.md)
- 외부: `github.com/jackc/pgx/v5`, `github.com/jackc/pgerrcode`

<br>

## 관련 이슈

- 이슈 #100 — DB-driven parsing rules
- 이슈 #134 — Claim Check 분리 (raw_contents 테이블)
- 이슈 #135 / #161 — validation_status 영속화
- 이슈 #173 — sample_urls 누적 / refiner
