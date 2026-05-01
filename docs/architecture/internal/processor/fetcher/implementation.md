# internal/processor/fetcher/implementation — Fetcher Backends

소스: [`internal/processor/fetcher/implementation/`](../../../../internal/processor/fetcher/implementation/)

[`domain/general`](domain.md) 의 Fetcher chain 에 link 되는 **실제 fetch 구현체**. 두 가지 전략 제공:

- [`goquery/`](../../../../internal/processor/fetcher/implementation/goquery/) — 정적 HTTP + goquery 파싱 (빠름/저비용)
- [`chromedp/`](../../../../internal/processor/fetcher/implementation/chromedp/) — 헤드리스 Chrome (CDP) 으로 JS 렌더 후 DOM 캡처

<br>

## goquery 구현

| 파일                                                                          | 역할                                                |
|------------------------------------------------------------------------------|-----------------------------------------------------|
| [crawler.go](../../../../internal/processor/fetcher/implementation/goquery/crawler.go)  | `Crawler` (=`core.Crawler` 구현) — Initialize/Start/Stop |
| [fetch.go](../../../../internal/processor/fetcher/implementation/goquery/fetch.go)      | `Fetch(ctx, target) → RawContent` — 표준 HTTP GET   |
| [parse.go](../../../../internal/processor/fetcher/implementation/goquery/parse.go)      | (legacy 용) goquery 기반 파싱 helper                 |
| [types.go](../../../../internal/processor/fetcher/implementation/goquery/types.go)      | `Config` — Timeout, UA, MaxBodySize 등              |

특성:
- 가벼움 — net/http + PuerkitoBio/goquery
- JS 실행 X → SPA / lazy-load 페이지는 빈 DOM 반환 가능
- Fetch chain 의 첫 번째 link 로 사용 (실패 시 chromedp 로 fallback)

<br>

## chromedp 구현

| 파일                                                                              | 역할                                                            |
|----------------------------------------------------------------------------------|-----------------------------------------------------------------|
| [crawler.go](../../../../internal/processor/fetcher/implementation/chromedp/crawler.go)     | `Crawler` 구현 — local Chrome 또는 remote (`ws://localhost:9222`) |
| [fetch.go](../../../../internal/processor/fetcher/implementation/chromedp/fetch.go)         | `Fetch` — Navigate + Wait + OuterHTML 캡처                       |
| [parse.go](../../../../internal/processor/fetcher/implementation/chromedp/parse.go)         | (legacy) chromedp 기반 파싱 helper                               |
| [graceful_timeout.go](../../../../internal/processor/fetcher/implementation/chromedp/graceful_timeout.go) | tab leak 방지 — context cancel 후에도 Chrome resource 회수 |
| [types.go](../../../../internal/processor/fetcher/implementation/chromedp/types.go)         | `Config`, `ChromedpOptions` (RemoteURL 등)                      |

특성:
- 비싸지만 SPA / dynamic page 에서 안정
- `make chrome-start` 가 `chromedp/headless-shell` 컨테이너 기동 (포트 9222)
- timeout / tab leak 처리는 `graceful_timeout.go` 가 단일 책임

<br>

## 의존

- [`internal/processor/fetcher/core`](core.md) — `Crawler`, `RawContent`, `Target`, `Config`
- [`internal/processor/fetcher/rate_limiter`](rate_limiter.md) — 요청 rate 제한 (host/IP 기준)
- 외부: `net/http`, `github.com/PuerkitoBio/goquery`, `github.com/chromedp/chromedp`

<br>

## 호출 측

- [`internal/processor/fetcher/domain/general/fetcher/goquery.go`](../../../../internal/processor/fetcher/domain/general/fetcher/goquery.go) — chain link 로 wrap
- [`internal/processor/fetcher/domain/general/fetcher/browser.go`](../../../../internal/processor/fetcher/domain/general/fetcher/browser.go) — chain link 로 wrap
- 사이트별 `Register` 함수가 둘을 결합한 chain 을 [`handler.Registry`](handler.md) 에 등록

본 패키지는 `core.Crawler` 인터페이스를 구현할 뿐이며, **chain 구성은 domain/general 책임**.

<br>

## 관련 이슈

- 이슈 #134 — fetcher / parser 분리 (Claim Check)
- 이슈 #175 — host 단위 fetcher rule + validation 기반 자동 chromedp 전환 (백로그)
