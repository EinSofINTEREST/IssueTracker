# internal/processor/validate — Content Validation Stage

소스: [`internal/processor/validate/`](../../../../internal/processor/validate/)

`issuetracker.normalized` 토픽을 consume 하여 Content 를 검증한 뒤 결과에 따라 분기:

- 통과 → `issuetracker.validated` 발행
- 실패 → `contents` row 삭제 + `issuetracker.dlq` 발행

<br>

## 구성

| 파일                                                                                 | 역할                                                            |
|-------------------------------------------------------------------------------------|-----------------------------------------------------------------|
| [validator.go](../../../../internal/processor/validate/validator.go)                 | `NewValidator(SourceType, ValidateConfig)` — news/community 분기 |
| [worker.go](../../../../internal/processor/validate/worker.go)                       | `Worker` — Kafka consumer + at-least-once 처리                  |
| [community/](../../../../internal/processor/validate/community/)                     | community 도메인 검증 규칙                                       |
| [news/](../../../../internal/processor/validate/news/)                               | news 도메인 검증 규칙                                            |

<br>

## Validator 분기

```go
switch sourceType {
case core.SourceTypeCommunity:
    return community.NewValidator(cfg)
default:
    return news.NewValidator(cfg)  // 기본 + 알 수 없는 타입
}
```

`ValidateConfig` 는 [`pkg/config.LoadValidate`](../../pkg/config.md) 가 제공 — 본문 길이, 신뢰도 임계값
등 환경변수로 조정.

<br>

## Worker 동작 (per message)

```
1. Kafka.FetchMessage (TopicNormalized)
   → ProcessingMessage → ContentRef
2. ContentService.GetByID(ref.ID) → core.Content
3. ProcessingLock.Acquire("validator", ref.URL)
4. Validator.Validate(ctx, content) → ValidationResult
   ├ Valid=true:
   │   ├ ContentService.UpdateValidationStatus(ref.ID, Passed) (이슈 #135 / #161)
   │   └ Producer.Publish(TopicValidated, ref)
   └ Valid=false:
       ├ ContentService.Delete(ref.ID)
       └ Producer.Publish(TopicDLQ, ref + error info)
5. drainTimeout (5s) 안에 Kafka.CommitMessages 시도 — at-least-once 보장
```

graceful shutdown 시 in-flight 메시지를 `drainTimeout` 동안 finalize.

<br>

## 의존

- [`internal/crawler/core`](../crawler/core.md)
- [`internal/locks`](../locks/README.md) — `ProcessingLock` (issuetracker 통합 모드) / `NoopProcessingLock` (processor 단독 모드)
- [`internal/storage/service`](../storage/service.md) — `ContentService`
- [`internal/storage`](../storage/README.md) — `ValidationStatus`
- [`pkg/queue`](../../pkg/queue.md), [`pkg/config`](../../pkg/config.md), [`pkg/logger`](../../pkg/logger.md)

<br>

## Wiring 위치

- 통합: [`cmd/issuetracker`](../../cmd/issuetracker.md) 단계 14
- 단독: [`cmd/processor`](../../cmd/processor.md)

<br>

## 외부 시스템

- Kafka: `issuetracker.normalized` consume / `issuetracker.validated` + `issuetracker.dlq` produce
- PostgreSQL: `contents` / `content_meta` (validation_status update / delete)
- Redis: ProcessingLock (단계="validator", 통합 모드만)

<br>

## 관련 이슈

- 이슈 #135 / #161 — validation_status 영속화
- 이슈 #178 — ProcessingLock 단계 prefix
- 이슈 #72 — graceful shutdown shutting_down 필드
