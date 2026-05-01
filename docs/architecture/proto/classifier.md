# proto/classifier — Classifier Service Definition

소스: [`proto/classifier/classifier.proto`](../../../proto/classifier/classifier.proto)

외부 ELArchive Classifier 서비스의 gRPC 인터페이스 정의. `make proto` 가 이 파일로부터
[`internal/classifier/grpc/pb/`](../../../internal/classifier/grpc/pb/) 의 Go stub 을 생성합니다.

<br>

## 패키지 / Go import path

```proto
syntax = "proto3";
package classifier;
option go_package = "issuetracker/internal/classifier/grpc/pb";
```

<br>

## 서비스

```proto
service ClassifierService {
  rpc Classify      (ClassifyRequest)       returns (ClassifyResponse);
  rpc ClassifyBatch (BatchClassifyRequest)  returns (BatchClassifyResponse);
  rpc Health        (HealthRequest)         returns (HealthResponse);
}
```

| RPC                | 설명                                                         |
|-------------------|--------------------------------------------------------------|
| `Classify`         | 단일 텍스트 → 카테고리 1건                                    |
| `ClassifyBatch`    | 복수 텍스트 (최대 100건) → 결과 배열 + total                  |
| `Health`           | 상태 / 모델 로드 여부 / default 카테고리 개수                  |

<br>

## 메시지

### Common

```proto
message CategoryInput {
  string name        = 1;  // 카테고리 식별자
  string description = 2;  // LLM 프롬프트에 포함될 설명
}

message ClassifyResult {
  string label      = 1;  // 예측 카테고리
  string reason     = 2;  // LLM 근거 (1문장)
  bool   parse_ok   = 3;  // 파싱 성공 여부
  string raw_output = 4;  // parse_ok=false 일 때 LLM 원본 출력
}
```

### Classify / Batch / Health

`ClassifyRequest`, `ClassifyResponse`, `BatchClassifyRequest`, `BatchClassifyItem`,
`BatchClassifyResponse`, `HealthRequest`, `HealthResponse` — 자세한 필드는
[classifier.proto](../../../proto/classifier/classifier.proto) 단일 소스.

<br>

## 코드 생성

```bash
make proto
```

[`Makefile`](../../../Makefile) 의 `proto` 타겟이 다음을 수행:
1. `protoc` / `protoc-gen-go` / `protoc-gen-go-grpc` 설치 확인 (없으면 install)
2. `protoc --proto_path=proto --go_out=. --go-grpc_out=. proto/classifier/classifier.proto`
3. 결과: `internal/classifier/grpc/pb/classifier.pb.go`, `classifier_grpc.pb.go`

생성된 파일은 **수정하지 않음** — proto 변경 후 `make proto` 재실행.

<br>

## 호출 측

- [`internal/classifier/grpc.Client`](../internal/classifier/grpc.md) — stub 을 wrap 한 클라이언트
- [`internal/classifier/Handler`](../internal/classifier/README.md) — gRPC 우선 + HTTP fallback

<br>

## 외부 서비스

ELArchive Classifier — IssueTracker 외부 별도 프로세스 (Python). gRPC `:50051`, HTTP `:8000`.
