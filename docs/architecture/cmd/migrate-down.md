# cmd/migrate-down — DB Migration Runner (rollback)

소스: [`cmd/migrate-down/main.go`](../../../cmd/migrate-down/main.go)
산출물: `bin/migrate-down` (`make build` 또는 `make pg-migrate-down`)

[`migrations.Rollback`](../../../migrations/) 를 호출해 마지막 마이그레이션을 되돌리는 thin CLI.
[`cmd/migrate`](migrate.md) 와 구조가 동일하며 호출하는 함수만 다릅니다.

<br>

## 역할

```
1. logger 초기화
2. config.Load() → DatabaseConfig
3. pgstore.NewPool() → pgx 연결 풀
4. migrations.Rollback(ctx, pool, log) — 적용된 마지막 마이그레이션 down 적용
```

<br>

## ⚠️ 운영 주의

- **운영 환경 전용** — dev 에서는 사용 지양 (Makefile help 명시)
- destructive — 데이터 손실 가능
- [.claude/rules/07-workflow.md](../../../.claude/rules/07-workflow.md) 의 destructive 영역으로 분류
  → AI 가 자율 실행하지 않음, 사용자 사전 확인 필요

<br>

## 의존 패키지

- [`internal/storage/postgres`](../../../internal/storage/postgres/)
- [`migrations`](../../../migrations/) — `Rollback(ctx, pool, log)`
- [`pkg/config`](../../../pkg/config/)
- [`pkg/logger`](../../../pkg/logger/)

<br>

## 사용

```bash
make pg-migrate-down   # 빌드 후 실행 (운영자 직접 트리거)
./bin/migrate-down     # 직접 실행
```
