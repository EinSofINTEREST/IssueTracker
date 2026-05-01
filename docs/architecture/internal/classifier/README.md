# internal/classifier — ELArchive Classifier Client

소스: [`internal/classifier/`](../../../../internal/classifier/)

외부 ELArchive Classifier 서비스 (텍스트 → 카테고리 분류) 를 호출하는 클라이언트. **gRPC 우선 + HTTP
fallback** 의 dual-protocol Handler 를 제공합니다.

> 현재 시점에는 파이프라인 내 실제 호출 지점이 없으나, enrichment / classify stage 도입 시 사용될
> 모듈로 미리 wiring 가능하도록 유지됩니다.

<br>

## 핵심 인터페이스

```go
type Classifier interface {
    Classify(ctx, text, []CategoryInput) (*ClassifyResponse, error)
    ClassifyBatch(ctx, []string, []CategoryInput) (*BatchClassifyResponse, error)
    Health(ctx) (*HealthResponse, error)
    Close() error
}
```

CategoryInput 생략 시 서비스의 기본 카테고리 (`configs/categories.yaml`) 사용.

<br>

## Handler 구조

[handler.go](../../../../internal/classifier/handler.go) 는 `grpc` 와 `http` 클라이언트를 모두 보유하고
`primary` 프로토콜 우선 호출 + 실패 시 `fallback`:

```
Handler.Classify
   ├ primary=gRPC → grpc.Classify
   │     ├ 성공 → 반환
   │     └ 실패 + fallback=true → http.Classify
   └ primary=HTTP → http.Classify
         └ (대칭)
```

<br>

## 서브패키지

| 디렉토리                                                           | 문서                                |
|-------------------------------------------------------------------|-------------------------------------|
| [`internal/classifier/grpc/`](../../../../internal/classifier/grpc/) | [grpc.md](grpc.md)                 |
| [`internal/classifier/http/`](../../../../internal/classifier/http/) | [http.md](http.md)                 |

생성된 gRPC stub 은 [`internal/classifier/grpc/pb/`](../../../../internal/classifier/grpc/pb/) 에 위치
([proto/classifier.md](../../proto/classifier.md) 의 `make proto` 가 생성).

<br>

## 의존

- [`pkg/logger`](../../pkg/logger.md)
- 외부: `google.golang.org/grpc`, `net/http`
- 외부 서비스: ELArchive Classifier (gRPC `:50051`, HTTP `:8000`)

<br>

## 관련 이슈

- 이슈 #142 — LLM chain (별개 시스템이지만 fallback 패턴이 유사)
