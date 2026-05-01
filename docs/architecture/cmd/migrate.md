# cmd/migrate — DB Migration Runner (forward)

소스: [`cmd/migrate/main.go`](../../../cmd/migrate/main.go)
산출물: `bin/migrate` (`make build` 또는 `make pg-migrate`)

PostgreSQL 스키마 마이그레이션을 **앞 방향으로** 적용하는 thin CLI. 실제 마이그레이션 로직은
[`migrations/`](../../../migrations/) 패키지가 보유하며, 본 바이너리는 단순히 그것을 호출합니다.

<br>

## 역할

```
1. logger 초기화 (Pretty=true, Level=info)
2. config.Load() → DatabaseConfig
3. pgstore.NewPool() → pgx 연결 풀
4. migrations.Run(ctx, pool, log) — 미적용 마이그레이션을 schema_migrations 기준으로 멱등 적용
```

실패 시 `Fatal` — 비-zero exit. CI 와 운영 entrypoint 가 이를 신호로 사용.

<br>

## 의존 패키지

- [`internal/storage/postgres`](../../../internal/storage/postgres/) — `NewPool`
- [`migrations`](../../../migrations/) — `Run(ctx, pool, log)` (단일 소스)
- [`pkg/config`](../../../pkg/config/) — `Load()` (DB 설정)
- [`pkg/logger`](../../../pkg/logger/)

<br>

## 사용

```bash
make pg-migrate   # 빌드 후 실행
./bin/migrate     # 직접 실행
```

마이그레이션 파일 추가 시 [`migrations/`](../../../migrations/) 에 추가 + 등록 — `cmd/migrate` 자체는
변경 불필요.

롤백은 [migrate-down.md](migrate-down.md) 참조.
