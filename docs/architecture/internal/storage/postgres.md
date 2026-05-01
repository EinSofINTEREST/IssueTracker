# internal/storage/postgres — pgx/v5 Implementation

소스: [`internal/storage/postgres/`](../../../../internal/storage/postgres/)

[Repository 인터페이스](README.md) 의 PostgreSQL 구현체. 모든 repo 가 단일 `pgxpool.Pool` 을 공유.

<br>

## 구성

| 파일                                                                          | 역할                                                                |
|------------------------------------------------------------------------------|---------------------------------------------------------------------|
| [db.go](../../../../internal/storage/postgres/db.go)                          | `NewPool(ctx, DBConfig, log)` — pgxpool 생성 + ping + 풀 설정        |
| [content.go](../../../../internal/storage/postgres/content.go)                | `ContentRepository` 구현 — 3-table 트랜잭션 (`contents` + `content_bodies` + `content_meta`) |
| [raw_content.go](../../../../internal/storage/postgres/raw_content.go)        | `RawContentRepository` 구현                                         |
| [parsing_rule.go](../../../../internal/storage/postgres/parsing_rule.go)      | `ParsingRuleRepository` 구현 (regex pattern 컬럼)                   |
| [sample_url.go](../../../../internal/storage/postgres/sample_url.go)          | `SampleURLRepository` 구현 (cap 100/rule)                           |
| [scanner.go](../../../../internal/storage/postgres/scanner.go)                | row → struct scan helper (시간/JSONB 변환)                          |

<br>

## Pool 설정

[db.go](../../../../internal/storage/postgres/db.go):
```go
poolCfg.MaxConns = cfg.MaxConns
poolCfg.MinConns = cfg.MinConns
poolCfg.MaxConnLifetime = 30 * time.Minute
poolCfg.MaxConnIdleTime = 10 * time.Minute
```

설정값은 [`pkg/config.DBConfig`](../../pkg/config.md) (ENV) 에서 로드.

<br>

## 테이블 스키마 (요약)

자세한 컬럼/인덱스는 [`migrations/`](../../../../migrations/) 가 단일 소스. 핵심 테이블:

### `contents`
- `id` (UUID), `url` (UNIQUE), `title`, `source`, `country`, `language`, `published_at`, `content_hash`, `created_at`
- 핵심 인덱스: `(country, published_at DESC)`, `(content_hash)`

### `content_bodies`
- `content_id` FK → `contents.id`
- `body` (TEXT, 큰 본문 분리)

### `content_meta`
- `content_id` FK
- `validation_status`, `reliability`, JSONB extra

### `raw_contents`
- Claim Check 임시 저장 — parser 가 처리 후 즉시 delete
- `id`, `url` (UNIQUE), `html` (TEXT), `status_code`, `headers` (JSONB), `fetched_at`

### `parsing_rules` (이슈 #100)
- `id`, `host_pattern`, `path_pattern` (regex), `target_type` (page/list)
- `selectors` (JSONB), `source_name` (`manual` / `llm-auto`), `enabled`, `created_at`, `updated_at`

### `parsing_rule_sample_urls` (이슈 #173 단계 4-1)
- `id`, `rule_id` FK, `url`, `observed_at`
- `(rule_id, url)` UNIQUE — 같은 rule 의 동일 URL 중복 방지
- per-rule cap 100 (`SampleCapPerRule`)

### `schema_migrations`
- 마이그레이션 버전 추적 (migrations 패키지가 관리)

<br>

## 의존

- [`internal/storage`](README.md) — 인터페이스 + 공유 타입
- [`internal/processor/fetcher/core`](../processor/fetcher/core.md) — `Content`, `RawContent`
- [`pkg/config`](../../pkg/config.md), [`pkg/logger`](../../pkg/logger.md)
- 외부: `github.com/jackc/pgx/v5`, `pgxpool`, `github.com/jackc/pgerrcode`

<br>

## Wiring 위치

[`cmd/issuetracker`](../../cmd/issuetracker.md), [`cmd/processor`](../../cmd/processor.md),
[`cmd/migrate`](../../cmd/migrate.md), [`cmd/migrate-down`](../../cmd/migrate-down.md) 모두
`pgstore.NewPool(ctx, dbCfg, log)` 로 시작.
