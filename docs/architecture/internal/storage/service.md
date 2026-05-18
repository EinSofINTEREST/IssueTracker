# internal/storage/service — Business Logic Layer

소스: [`internal/storage/service/`](../../../../internal/storage/service/)

[Repository 인터페이스](README.md) 위에 **순수 CRUD 이상의 로직** (중복 감지 / 일괄 저장 / LLM 결정 처리 / Decorator chain 자동 합성) 을 제공하는 service 계층. 이슈 #431 (Phase 2 layering) 으로 BlacklistService + ParserRuleService 가 추가되어, 호출자 (예: llmgen.Generator) 는 service interface 만 의존하고 repository 직접 의존을 제거 (의존성 역전).

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
    // status: "passed" / "rejected" 등 ValidationStatus 의 string 표현
    // code:   분류 코드 (예: "VAL_001"); 빈 문자열 허용
    // detail: 자유 형식 사유; 빈 문자열 허용
    UpdateValidationStatus(ctx context.Context, id, status, code, detail string) error
}
```

**중복 감지**: ContentHash (제목+본문 해시) 가 동일하면 기존 row 유지 — Title/PublishedAt 갱신만 또는 완전 무시. 결정 로직은 [content.go](../../../../internal/storage/service/content.go) 에 명시.

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

    List(ctx, filter model.RawContentFilter) ([]*core.RawContent, error)
    PurgeOlderThan(ctx, t time.Time) (int64, error)  // cleanup cron 용
}
```

ParserWorker (이슈 #134) 가 파싱 완료된 raw row 를 즉시 `Delete` — Claim Check 패턴.

<br>

## BlacklistService (이슈 #431, #480)

[blacklist.go](../../../../internal/storage/service/blacklist.go):

```go
type BlacklistService interface {
    // HandleLLMDecision: LLM blacklist 판정 → parser_blacklist 자동 등록 (이슈 #326, #480)
    //
    //   - sample URL → ^/$ anchor regex 변환 (host-wide catch-all over-reach 회피)
    //   - mode 인자가 빈 문자열이면 default "drop" — backward compat (이슈 #480)
    //   - ErrDuplicate → (false, nil) graceful 흡수
    HandleLLMDecision(ctx, host, sampleURL string, targetType model.TargetType,
                       reason string, mode model.BlacklistMode) (inserted bool, err error)

    // CRUD 위임
    Insert(ctx, *model.BlacklistRecord) error
    Update(ctx, *model.BlacklistRecord) error
    Delete(ctx, id int64) error
    GetByID(ctx, id int64) (*model.BlacklistRecord, error)
    FindEnabledByHost(ctx, host string) ([]*model.BlacklistRecord, error)
    List(ctx, filter model.BlacklistFilter) ([]*model.BlacklistRecord, error)
}
```

### 책임

- **LLM 결정 처리** — 기존 `llmgen.Generator.handleBlacklistDecision` 의 비즈니스 로직을 본 service 로 흡수 (이슈 #431)
  - sample URL 의 path 를 `regexp.QuoteMeta` + `^/$` anchor 한 **정확 일치** regex 로 path_pattern 생성
  - URL parse 실패 → INSERT skip (host-wide catch-all over-reach 회피)
  - `mode` 인자 인식 (`drop` / `extract_links_only`) — 빈/unknown 값은 drop 으로 fallback + WARN 로그 (이슈 #480)
  - `ErrDuplicate` (이미 등록) → `(false, nil)` graceful 흡수
- **Decorator chain 자동 합성** — wiring 측 boilerplate 제거 (`WithBlacklistQueryTimeout`, `WithBlacklistInvalidator`)
- **CRUD 위임** — repository 직접 호출

### 생성 옵션

```go
blacklistSvc := service.NewBlacklistService(
    blacklistRepoRaw, log,
    service.WithBlacklistQueryTimeout(dbCfg.QueryTimeout),  // timeout decorator
    service.WithBlacklistInvalidator(blacklistMatcher),     // cache invalidator decorator
)
```

내부에서 `repo → invalidator → timeout → service` 순으로 decorator chain 합성.

<br>

## ParserRuleService (이슈 #431)

[parser_rule.go](../../../../internal/storage/service/parser_rule.go):

```go
type ParserRuleService interface {
    Insert(ctx, *model.ParserRuleRecord) error
    Update(ctx, *model.ParserRuleRecord) error
    UpdatePathPattern(ctx, id int64, pattern, description string) error
    GetByID(ctx, id int64) (*model.ParserRuleRecord, error)
    FindActive(ctx, host string, targetType model.TargetType) (*model.ParserRuleRecord, error)
    InsertNextVersion(ctx, *model.ParserRuleRecord) error
    HasAnyRule(ctx, hostPattern string, targetType model.TargetType) (exists, hasEnabled bool, err error)
    FindByNaturalKey(ctx, sourceName, hostPattern, pathPattern string,
                     targetType model.TargetType, version int) (*model.ParserRuleRecord, error)
    FindActiveCandidates(ctx, host string, targetType model.TargetType) ([]*model.ParserRuleRecord, error)
    List(ctx, filter model.ParserRuleFilter) ([]*model.ParserRuleRecord, error)
    Delete(ctx, id int64) error
}
```

### 책임

- **Decorator chain 합성** — `WithParserRuleQueryTimeout`, `WithParserRuleInvalidator` 옵션으로 자동 wrap
- **CRUD 위임** — 현 시점에선 단순 facade. 향후 stale 재학습 / version fallback 등 비즈니스 로직 추가 예정

### 생성 옵션

```go
parserRuleSvc := service.NewParserRuleService(
    parserRuleRepoRaw, log,
    service.WithParserRuleQueryTimeout(dbCfg.QueryTimeout),
    service.WithParserRuleInvalidator(ruleResolver),
)
```

<br>

## 의존

- [`internal/storage/repository`](README.md) — Repository 인터페이스
- [`internal/storage/decorator`](README.md) — Wrap*WithTimeout / Wrap*WithInvalidator
- [`internal/storage/model`](README.md) — `BlacklistRecord`, `ParserRuleRecord`, `BlacklistMode`, etc.
- [`internal/processor/fetcher/core`](../processor/fetcher/core.md) — `Content`, `RawContent`
- [`pkg/logger`](../../pkg/logger.md)

<br>

## 호출 측

| 호출자 | 사용 메소드 | 비고 |
|---|---|---|
| [processor/fetcher/domain/general.ChainHandler](../processor/fetcher/domain.md) | `RawContentService.Store` | Claim Check 저장 |
| [parser/worker.Worker](../processor/parser/README.md) | `RawContentService.GetByID` / `Delete`, `ContentService.Store` | Claim Check 로드 + content 저장 |
| [parser/worker.RawContentCleaner](../processor/parser/README.md) | `RawContentService.PurgeOlderThan` | 오래된 raw 정리 cron |
| [processor/validate.Worker](../processor/validate.md) | `ContentService.GetByID` / `Delete` / `UpdateValidationStatus` | 검증 결과 영속화 (이슈 #135/#161) |
| [parser/rule.llmgen.Generator](../processor/parser/rule.md#4-llm-selector-generator) | `BlacklistService.HandleLLMDecision` (의존성 역전, 이슈 #431) | LLM blacklist 판정 자동 등록 |
| [parser/rule.autoDemoter](../processor/parser/rule.md#2-rule-engine) | `BlacklistService.Insert` (좁은 `AutoDemoteRegisterer` interface 만 의존, 이슈 #477) | index-only 휴리스틱 자동 강등 |
| [processor/precheck.Source](../processor/precheck.md) | `BlacklistService.FindEnabledByHost` | URL 처리 가부 판단 (이슈 #425) |

<br>

## 관련 이슈

- 이슈 #134 — Claim Check (Delete idempotent)
- 이슈 #135 / #161 — validation_status 영속화
- 이슈 #326 — LLM auto-blacklist 등록 (HandleLLMDecision 의 전신)
- **이슈 #431 — Phase 2 service layer** (BlacklistService + ParserRuleService 신설, decorator chain 자동 합성)
- 이슈 #432 — storage 도메인 audit (Phase 3 — 단순 CRUD service 회의적, 직접 호출 정책 검토)
- 이슈 #477 — auto_demote.autoDemoter 가 BlacklistService.Insert 호출 (좁은 interface 의존)
- **이슈 #480 — `HandleLLMDecision` mode 인자 추가** (drop / extract_links_only 분기)
