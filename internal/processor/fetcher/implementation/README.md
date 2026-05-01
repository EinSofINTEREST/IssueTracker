# Crawler Implementations

## 구조

```
implementation/
├── goquery/           # 정적 크롤링 (goquery)
│   ├── types.go       # GoqueryCrawler 타입 정의
│   ├── crawler.go     # 생성자 + 생명주기
│   ├── fetch.go       # Fetch (HTTP → RawContent)
│   └── parse.go       # FetchAndParse (HTTP → Article)
└── chromedp/          # 동적 크롤링 (headless browser)
    ├── types.go       # ChromedpCrawler 타입 및 옵션 정의
    ├── crawler.go     # 생성자 + 생명주기 + 브라우저 관리
    ├── fetch.go       # Fetch (브라우저 렌더링 → RawContent)
    └── parse.go       # FetchAndParse + EvaluateJS
```

## 크롤러 선택 기준

| 조건 | 크롤러 |
|------|--------|
| 정적 HTML (뉴스, RSS) | `goquery` |
| JavaScript 렌더링 필요 (SPA, 커뮤니티) | `chromedp` |
| API 기반 소스 | `goquery` (JSON 파싱) |
| 무한 스크롤, 동적 로딩 | `chromedp` |

## 사용 예제

### goquery (정적)

```go
crawler := goquery.NewGoqueryCrawler("cnn", sourceInfo, config)
crawler.Initialize(ctx, config)

raw, err := crawler.Fetch(ctx, target)
article, err := crawler.FetchAndParse(ctx, target, selectors)
```

### chromedp (동적)

```go
crawler := chromedp.NewChromedpCrawler("dcinside", sourceInfo, config)
crawler.Initialize(ctx, config)
defer crawler.Stop(ctx)  // 브라우저 리소스 해제 필수

// 기본 사용
raw, err := crawler.Fetch(ctx, target)
article, err := crawler.FetchAndParse(ctx, target, selectors)

// JS 실행
title, err := crawler.EvaluateJS(ctx, url, "document.title")

// 옵션 커스터마이징
opts := chromedp.ChromedpOptions{
  Headless:     true,
  WaitSelector: ".article-body",  // 특정 요소 대기
  WaitStable:   true,
}
crawler := chromedp.NewChromedpCrawlerWithOptions("name", source, config, opts)
```
