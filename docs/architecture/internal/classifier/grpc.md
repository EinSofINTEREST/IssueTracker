# internal/classifier/grpc — gRPC Client

소스: [`internal/classifier/grpc/`](../../../../internal/classifier/grpc/)

[`proto/classifier/classifier.proto`](../../../../proto/classifier/classifier.proto) 의 service stub
을 wrap 하여 [Classifier 인터페이스](README.md) 를 만족하는 클라이언트를 제공합니다.

<br>

## 구성

| 파일                                                                        | 역할                                                  |
|----------------------------------------------------------------------------|-------------------------------------------------------|
| [client.go](../../../../internal/classifier/grpc/client.go)                 | `Client` — Dial / Classify / ClassifyBatch / Health / Close |
| [pb/classifier.pb.go](../../../../internal/classifier/grpc/pb/classifier.pb.go) | proto 메시지 stub (자동 생성)                       |
| [pb/classifier_grpc.pb.go](../../../../internal/classifier/grpc/pb/classifier_grpc.pb.go) | service stub (자동 생성)                  |

`pb/` 는 [`make proto`](../../../../Makefile) (`protoc + protoc-gen-go + protoc-gen-go-grpc`) 가
자동 생성 — **수정하지 않음**.

<br>

## 사용

```go
client, err := grpc.NewClient(grpc.Config{Endpoint: "localhost:50051", Timeout: 5*time.Second})
defer client.Close()

resp, err := client.Classify(ctx, "오늘의 정치 뉴스", nil)
```

[Handler](README.md) 가 본 클라이언트와 [HTTP 클라이언트](http.md) 를 묶어 fallback 처리.

<br>

## 의존

- 자동 생성 stub (`pb/`)
- `google.golang.org/grpc`
- [`pkg/logger`](../../pkg/logger.md)

<br>

## proto 변경 시

1. [`proto/classifier/classifier.proto`](../../../../proto/classifier/classifier.proto) 수정
2. `make proto` 실행 → `pb/*.go` 재생성
3. `client.go` 가 새 메시지/메소드 사용하도록 수정
