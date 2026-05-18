# internal/processor/precheck — Common URL Processing Gate

소스: [`internal/processor/precheck/`](../../../../internal/processor/precheck/)

fetcher / parser / 기타 processor stage 가 URL 을 실제로 처리하기 전에 **처리 가부** 를 일괄 판정하는 공용 진입 게이트. 이슈 #425 (PR #436) 으로 도입.

이전에는 blacklist 매칭이 publisher 단계에서만 일어났으나, fetcher 가 publish 한 URL 이 parser 단계에서 다시 한 번 정책 적용을 받아야 하는 케이스가 늘어나면서 cross-cutting 정책을 단일 boundary 로 모음.

<br>

## 설계 목표

- **다중 stage 진입점에서 동일 게이트 호출** — fetch / parse / outgoing chained URL filter 등
- **cross-cutting 정책의 plug-in** — blacklist / 향후 rate_limit / robots / domain throttle 등
- **의존성 역전** — Source 는 의존 구현체 (BlacklistMatcher / rate_limit) 를 본 패키지가 모름. 호출자가 wiring 시 주입

<br>

## 핵심 타입

[`precheck.go`](../../../../internal/processor/precheck/precheck.go):

```go
// 단일 URL 의 처리 결정
type Verdict int

const (
    VerdictAllow              Verdict = iota  // 정상 진행
    VerdictDrop                              // fetch / parse 스킵, commit-only
    VerdictExtractLinksOnly                   // list 강제 분기 (fetch + ParseLinks 만, ParsePage skip)
                                              // blacklist 도메인의 'extract_links_only' mode 와 동일
)

// Source / Decider 의 판정 결과
type Decision struct {
    Verdict Verdict
    Source  string   // 어떤 Source 가 판정했는지 (운영 가시성)
    Reason  string   // 사후 분류 / 검증
}

// 단일 정책 plug-in interface
type Source interface {
    Name() string  // 식별자 (예: "blacklist") — Decision.Source 로 들어감
    Check(ctx context.Context, rawURL string) Decision
}

// 다중 Source 를 합성한 게이트 — interface (struct 아님)
type Decider interface {
    CheckURL(ctx context.Context, rawURL string) Decision
}
func New(sources ...Source) Decider
```

`Decider.CheckURL` 은 등록된 Source 들을 순차 호출 — 첫 non-Allow Verdict 가 나오면 즉시 반환. 모두 Allow 면 `VerdictAllow`.

<br>

## Source 구현체

[`blacklist.go`](../../../../internal/processor/precheck/blacklist.go):

```go
// parser_blacklist 매칭을 precheck Source 로 노출
// matcher 가 nil 이면 NewBlacklistSource 가 nil 반환 (graceful disable)
func NewBlacklistSource(matcher *rule.BlacklistMatcher) Source
```

- BlacklistMatcher 의 mode 별 결과를 Verdict 로 매핑:
  - `BlacklistModeDrop` → `VerdictDrop`
  - `BlacklistModeExtractLinksOnly` → `VerdictExtractLinksOnly`
  - 매칭 없음 → `VerdictAllow`

향후 추가 가능 (계획):
- `rate_limit.Source` — host 단위 token bucket
- `robots.Source` — robots.txt 정책
- `domain_throttle.Source` — 글로벌 도메인 사용 한도

<br>

## 호출 패턴

```go
// wiring (cmd/issuetracker)
blacklistSrc := precheck.NewBlacklistSource(blacklistMatcher)  // matcher nil 이면 nil
preChecker := precheck.New(blacklistSrc)  // nil Source 는 자동 skip

// 호출 (publisher / parser_worker / fetcher 진입점)
verdict := preChecker.CheckURL(ctx, jobURL)
switch verdict.Verdict {
case precheck.VerdictAllow:
    // 정상 진행
case precheck.VerdictDrop:
    // commit-only, 처리 skip + 운영 로그
case precheck.VerdictExtractLinksOnly:
    // 파서 단계에서 list 강제 분기 — fetch 는 진행, parse 시 ParseLinks 만
}
```

<br>

## 의존

- [`internal/processor/parser/rule`](parser/rule.md) — `BlacklistMatcher` (BlacklistSource 의 백엔드)
- [`internal/storage/model`](../storage/README.md) — `BlacklistMode` 상수

<br>

## 호출 측

| 호출자 | 사용 메소드 | 비고 |
|---|---|---|
| [`internal/publisher`](../publisher.md) | `Decider.CheckURL` | publish 직전 마지막 게이트 |
| [`internal/processor/parser/worker`](parser/README.md) | `Decider.CheckURL` | parser 진입점 — list / page 라우팅 결정 (이미 wiring 완료, main.go) |
| [`internal/processor/fetcher/handler`](fetcher/handler.md) | `Decider.CheckURL` | fetch 시작 전 게이트 (이미 wiring 완료, main.go) |

<br>

## Wiring 위치

[`cmd/issuetracker/main.go`](../../../../cmd/issuetracker/main.go) — blacklistMatcher / 기타 Source 구성 후 `precheck.New` 로 합성. `BLACKLIST_ENABLED=false` 환경에서는 `NewBlacklistSource(nil)` 가 nil 반환 → `Decider` 가 빈 Source 리스트라 모든 URL Allow (기능 OFF).

<br>

## 관련 이슈

- **이슈 #425 — precheck 패키지 신설** (다중 stage 진입점 게이트)
- 이슈 #295 / #297 — parser_blacklist (drop / extract_links_only mode 도메인)
- 이슈 #431 — BlacklistService (decorator chain + matcher invalidate)
- 이슈 #477 — auto_demote (parser_blacklist 자동 등록, mode='extract_links_only')
- 이슈 #480 — LLM auto-blacklist mode 분기 (drop / extract_links_only 등록)
