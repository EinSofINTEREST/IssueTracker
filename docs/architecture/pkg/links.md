# pkg/links — URL Normalization & Extraction

소스: [`pkg/links/`](../../../pkg/links/)

URL 정규화 (`Normalizer`) 와 페이지 내 `<a>` 추출 (`Extractor`) 을 제공합니다. 중복 감지 / 큐 dedup /
ingestion lock 의 일관성을 위해 모든 진입점이 동일 Normalizer 를 통과합니다.

<br>

## 구성

| 파일                                               | 역할                                                  |
|--------------------------------------------------|-------------------------------------------------------|
| [normalizer.go](../../../pkg/links/normalizer.go)  | `Normalizer` — Normalize(url) + functional options    |
| [extractor.go](../../../pkg/links/extractor.go)    | goquery 기반 `<a href>` 추출 helper                   |

<br>

## Normalizer

```go
n := links.NewNormalizer(opts...)
canonical, err := n.Normalize("https://www.Example.com/path/?utm_source=x#frag")
// → "https://example.com/path"
```

**기본 정책**:
- 스킴 lowercase (`HTTP` → `http`)
- HTTPS 강제 옵션 (또는 원본 유지)
- `www.` 제거
- trailing slash 제거
- fragment (`#…`) 제거
- query 파라미터: host 별 allow-list 외 제거 (utm_*, fbclid 등 추적 파라미터 제거)

<br>

## Extractor

[extractor.go](../../../pkg/links/extractor.go) — HTML + base URL → 절대 URL 리스트. 구현 자체는 generic 이며,
boundary 변환은 [`internal/crawler/core.HTMLLinkExtractor`](../internal/crawler/core.md) 가 수행:

```go
// pkg/links (generic util — fmt.Errorf wrap)
extracted, err := pkgLinks.Extract(html, baseURL)

// internal/crawler/core (boundary — CrawlerError 변환)
func (e *HTMLLinkExtractor) Extract(raw *RawContent) ([]Link, error) {
    res, err := e.inner.Extract(raw.HTML, raw.URL)
    if err != nil {
        return nil, NewParseError(CodeParseHTML, "...", raw.URL, err)
    }
    ...
}
```

<br>

## 의존

- 외부: 표준 `net/url` + `github.com/PuerkitoBio/goquery`
- 본 패키지가 import 하는 다른 패키지: 없음 (sink 노드)

<br>

## 호출 측

- [`internal/publisher`](../internal/publisher.md) — Publisher.SetNormalizer
- [`internal/crawler/parser/rule/discovery`](../internal/crawler/parser.md) — full-page link discovery
- [`internal/crawler/core/extractor`](../internal/crawler/core.md) — boundary wrapper
