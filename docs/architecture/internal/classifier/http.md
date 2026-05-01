# internal/classifier/http — HTTP REST Client

소스: [`internal/classifier/http/`](../../../../internal/classifier/http/)

ELArchive Classifier 의 HTTP REST 인터페이스를 호출하는 클라이언트. [Classifier 인터페이스](README.md)
를 만족합니다.

<br>

## 구성

| 파일                                                              | 역할                                                  |
|------------------------------------------------------------------|-------------------------------------------------------|
| [client.go](../../../../internal/classifier/http/client.go)       | `Client` — Classify / ClassifyBatch / Health / Close   |

엔드포인트:
- `POST /v1/classify`
- `POST /v1/classify_batch`
- `GET  /v1/health`

요청/응답 JSON 스키마는 proto 메시지와 1:1 대응 (Classifier 서비스 측 단일 소스).

<br>

## 사용

```go
client := http.NewClient(http.Config{BaseURL: "http://localhost:8000", Timeout: 10*time.Second})
defer client.Close()

resp, err := client.Classify(ctx, "오늘의 정치 뉴스", nil)
```

<br>

## 의존

- 표준 `net/http`
- [`pkg/logger`](../../pkg/logger.md)

<br>

## gRPC 와의 관계

[Handler](README.md) 의 fallback 경로 — gRPC 가 unavailable 일 때 사용. 직접 사용보다 Handler 를 통해
사용하는 것이 권장.
