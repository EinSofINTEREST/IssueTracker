# internal/processor — Pipeline Stage Interface + Implementations

소스: [`internal/processor/`](../../../../internal/processor/)

파이프라인 단계 (fetcher / parser / validate) 의 공통 lifecycle 인터페이스를 정의하고, 각 단계의
실구현 패키지 (`fetcher/`, `parser/`, `validate/`) 를 보유합니다.

<br>

## Stage 인터페이스 (이슈 #206)

[processor.go](../../../../internal/processor/processor.go):

```go
// 모든 파이프라인 단계가 구현하는 lifecycle 인터페이스.
type Stage interface {
    Name() string                              // "fetcher" / "parser" / "validate"
    Start(ctx context.Context)                 // 비-blocking, worker goroutine 기동
    Stop(ctx context.Context) error            // graceful shutdown 대기
}
```

`cmd/issuetracker/main.go` 는 모든 단계를 `[]processor.Stage` 로 균일하게 Start/Stop:

```go
stages := []processor.Stage{
    fetcher.NewStage(manager),
    parserStage.NewStage(pw, cleaner, llmGen, pathRefiner, log),
    validate.NewStage(validateWorker),
}
for _, s := range stages { s.Start(ctx) }
// shutdown 은 정의 역순
for i := len(stages)-1; i >= 0; i-- { stages[i].Stop(shutdownCtx) }
```

stage 추가 (예: enrich, embed) 시 `Stage` 인터페이스만 만족하면 main wiring 자동 통합.

<br>

## 단계별 Stage 구현 위치

| Stage | Wrapper 위치 | wrapping 대상 |
|---|---|---|
| `fetcher` | [`fetcher/stage.go`](../../../../internal/processor/fetcher/stage.go) | `worker.PoolManager` (단일) |
| `parser` | [`parser/stage/stage.go`](../../../../internal/processor/parser/stage/stage.go) | `worker.ParserWorker` + `worker.RawContentCleaner` + `llmgen.Generator` (선택) + `refiner.Refiner` (선택) — 여러 background goroutine 묶음 |
| `validate` | [`validate/stage.go`](../../../../internal/processor/validate/stage.go) | `validate.Worker` (단일) |

> **`parser/stage` 가 sub-package 인 이유:** `parser/parser.go` 의 `Page` 타입을 `rule/*` 가 import 하므로,
> parser 부모 패키지가 `rule/*` 를 import 하면 cycle 발생. stage 를 `parser/stage/` 로 분리하여 회피.

<br>

## 검증 타입 (validate 단계 공유 hub)

`Validator` / `ValidationResult` / `ValidationError` 는 `validate/news/` + `validate/community/`
sub-validator 가 공유하는 타입이라 본 패키지에 잔류 (validate/ 로 옮기면 sub-package 가 부모를 import → cycle).

```go
type Validator interface {
    Validate(ctx context.Context, content *core.Content) ValidationResult
}

type ValidationResult struct {
    IsValid      bool
    QualityScore float32  // 0.0 ~ 1.0; 0.5 미만이면 DLQ 라우팅 대상
    Errors       []ValidationError
}
```

<br>

## 서브패키지

| 디렉토리                                                                       | 문서                                |
|-------------------------------------------------------------------------------|-------------------------------------|
| [`internal/processor/fetcher/`](../../../../internal/processor/fetcher/)       | [fetcher/README.md](fetcher/README.md) |
| [`internal/processor/parser/`](../../../../internal/processor/parser/)         | [parser/README.md](parser/README.md)   |
| [`internal/processor/validate/`](../../../../internal/processor/validate/)     | [validate.md](validate.md)             |

<br>

## 향후 확장

- `internal/processor/enrich/` — entity / sentiment / topic
- `internal/processor/embed/` — vector embedding
- `internal/processor/classify/` — [internal/classifier](../classifier/README.md) 호출 stage

새 stage 추가 시:
1. `internal/processor/<stage>/` 디렉토리 생성
2. worker 구현
3. `<stage>/stage.go` (또는 sub-package 의 `stage/`) 에 `processor.Stage` 구현
4. `cmd/issuetracker/main.go` 의 `stages` 슬라이스에 추가
