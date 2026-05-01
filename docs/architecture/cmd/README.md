# cmd/ — Entry Points

[`cmd/`](../../../cmd/) 는 모든 실행 바이너리의 main 패키지를 보관합니다. 각 서브디렉토리는
하나의 바이너리에 대응하며 `make build` 로 [`bin/`](../../../bin/) 에 빌드됩니다.

엔트리는 비즈니스 로직을 담지 않고 [`internal/`](../../../internal/) 와 [`pkg/`](../../../pkg/)
의 패키지를 wire 하는 역할만 합니다 (의존성 역전).

<br>

## 바이너리 일람

| 디렉토리                         | 산출물                | 역할                                         | 문서                                        |
|---------------------------------|---------------------|---------------------------------------------|---------------------------------------------|
| [`cmd/issuetracker/`](../../../cmd/issuetracker/) | `bin/issuetracker`  | **통합 파이프라인** — 모든 stage 를 단일 프로세스로 실행 | [issuetracker.md](issuetracker.md)          |
| [`cmd/processor/`](../../../cmd/processor/)       | `bin/processor`     | **검증 단독 실행** — `issuetracker.normalized` → `issuetracker.validated` | [processor.md](processor.md)                |
| [`cmd/migrate/`](../../../cmd/migrate/)           | `bin/migrate`       | DB 마이그레이션 적용 (forward)                | [migrate.md](migrate.md)                    |
| [`cmd/migrate-down/`](../../../cmd/migrate-down/) | `bin/migrate-down`  | DB 마이그레이션 롤백 (배포 환경 전용)         | [migrate-down.md](migrate-down.md)          |
| [`cmd/api/`](../../../cmd/api/)                   | (미구현)             | REST/GraphQL API 서버 — placeholder           | (계획)                                       |
| [`cmd/rldebug/`](../../../cmd/rldebug/)           | (미구현)             | Rate limiter 디버깅 도구 — placeholder        | (계획)                                       |

새 바이너리 추가 시 [`Makefile`](../../../Makefile) 의 `build` 타겟과 `*_BINARY` 변수를 동시 갱신
([01-architecture.md](../../../.claude/rules/01-architecture.md) 의 cmd/ 규칙).

<br>

## 빌드 / 실행

```bash
make build              # 전체 빌드
make run-issuetracker   # 통합 실행 (chrome + kafka 자동 기동, scripts/entrypoint.sh)
make run-processor      # 검증 단독 실행
make pg-migrate         # 마이그레이션 적용
make pg-migrate-down    # 마이그레이션 롤백 (운영자 전용)
```

<br>

## 의존 관계 (요약)

```
cmd/issuetracker  ──→ internal/* (전부) + pkg/* (전부)
cmd/processor     ──→ internal/{processor/validate, storage/*, locks(NoopProcessingLock)} + pkg/*
cmd/migrate       ──→ internal/storage/postgres + migrations
cmd/migrate-down  ──→ internal/storage/postgres + migrations
```
