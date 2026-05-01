# internal/processor — Processing Pipeline Stage Interfaces

소스: [`internal/processor/`](../../../../internal/processor/)

처리 단계 (validate / enrich / classify 등) 의 공통 인터페이스를 정의하고, 현재 시점의 단일 구현인
검증(validate) 단계 패키지를 보유합니다.

<br>

## 핵심 인터페이스

[processor.go](../../../../internal/processor/processor.go):

```go
type ContentProcessor interface {
    Process(ctx context.Context, c *core.Content) (*core.Content, error)
}

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

| 디렉토리                                                                  | 문서                                |
|--------------------------------------------------------------------------|-------------------------------------|
| [`internal/processor/validate/`](../../../../internal/processor/validate/) | [validate.md](validate.md)         |

<br>

## 향후 확장

- `internal/processor/enrich/` — entity / sentiment / topic
- `internal/processor/embed/` — vector embedding
- `internal/processor/classify/` — [internal/classifier](../classifier/README.md) 호출 stage
