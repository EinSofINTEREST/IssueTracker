# internal/storage/service — Business Logic Layer

소스: [`internal/storage/service/`](../../../../internal/storage/service/)

[Repository 인터페이스](README.md) 위에 **순수 CRUD 이상의 로직** (중복 감지 / 일괄 저장 / 상태 업데이트)
을 제공하는 service 계층.

<br>

## ContentService

[content.go](../../../../internal/storage/service/content.go):

```go
type ContentService interface {
    // ContentHash 동일 시 기존 ID 반환 (저장 X)
    Store(ctx, *core.Content) (id string, isDuplicate bool, err error)

    StoreBatch(ctx, []*core.Content) ([]StoreResult, error)
    GetByID(ctx, id string) (*core.Content, error)
    Delete(ctx, id string) error

    // validation_status 업데이트 (이슈 #135 / #161)
    // status: "passed" / "rejected" 등 storage.ValidationStatus 의 string 표현
    // code:   분류 코드 (예: "VAL_001"); 빈 문자열 허용
    // detail: 자유 형식 사유 (예: "title length < 10"); 빈 문자열 허용
    UpdateValidationStatus(ctx context.Context, id, status, code, detail string) error
}
```

**중복 감지**: ContentHash (제목+본문 해시) 가 동일하면 기존 row 유지 — Title/PublishedAt 갱신만 또는
완전 무시. 결정 로직은 [content.go](../../../../internal/storage/service/content.go) 에 명시.

<br>

## RawContentService

[raw_content.go](../../../../internal/storage/service/raw_content.go):

```go
type RawContentService interface {
    // 동일 URL 시 기존 ID 반환
    Store(ctx, *core.RawContent) (id string, isDuplicate bool, err error)

    GetByID(ctx, id string) (*core.RawContent, error)

    // idempotent (없어도 nil) — Claim Check 정리 (이슈 #134)
    Delete(ctx, id string) error

    List(ctx, filter storage.RawContentFilter) ([]*core.RawContent, error)
    PurgeOlderThan(ctx, t time.Time) (int64, error)  // cleanup cron 용
}
```

ParserWorker (이슈 #134) 가 파싱 완료된 raw row 를 즉시 `Delete` — Claim Check 패턴.

<br>

## 의존

- [`internal/storage`](README.md) — Repository 인터페이스 + 공유 타입
- [`internal/processor/fetcher/core`](../processor/fetcher/core.md) — `Content`, `RawContent`
- [`pkg/logger`](../../pkg/logger.md)

<br>

## 호출 측

| 호출자                                              | 사용 메소드                                       |
|----------------------------------------------------|--------------------------------------------------|
| [processor/fetcher/domain/general.ChainHandler](../processor/fetcher/domain.md) | `RawContentService.Store` (Claim Check)   |
| [parser/worker.ParserWorker](../processor/parser/README.md)   | `RawContentService.GetByID` / `Delete`<br>`ContentService.Store` |
| [parser/worker.RawContentCleaner](../processor/parser/README.md) | `RawContentService.PurgeOlderThan`            |
| [processor/validate.Worker](../processor/validate.md) | `ContentService.GetByID` / `Delete` / `UpdateValidationStatus` |

<br>

## 관련 이슈

- 이슈 #134 — Claim Check (Delete idempotent)
- 이슈 #135 / #161 — validation_status 영속화
