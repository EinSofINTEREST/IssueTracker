package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"issuetracker/internal/crawler/core"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
	"issuetracker/internal/crawler/implementation/goquery"
	"issuetracker/pkg/logger"
)

const (
	divider = "────────────────────────────────────────────────────"
	testURL = "https://httpbin.org/html"
)

// selectors: httpbin.org/html 페이지에 맞는 CSS 셀렉터
var selectors = map[string]string{
	"title":  "h1",
	"body":   "p",
	"images": "img",
}

func main() {
	log := logger.New(logger.Config{
		Level:  logger.LevelWarn, // 예제 출력이 보이도록 라이브러리 로그 억제
		Pretty: true,
	})
	ctx := log.ToContext(context.Background())

	config := core.Config{
		UserAgent:       "IssueTracker/1.0 (+https://example.com/bot) Go-http-client",
		Timeout:         30 * time.Second,
		MaxRetries:      3,
		RequestsPerHour: 100,
		BurstSize:       10,
		MaxIdleConns:    100,
		MaxConnsPerHost: 10,
	}

	sourceInfo := core.SourceInfo{
		Name:     "httpbin",
		Country:  "US",
		Type:     core.SourceTypeNews,
		BaseURL:  "https://httpbin.org",
		Language: "en",
	}

	target := core.Target{
		URL:  testURL,
		Type: core.TargetTypeArticle,
	}

	fmt.Println(divider)
	fmt.Println("  IssueTracker - 크롤러 사용 예제 (Basic Usage)")
	fmt.Println(divider)
	fmt.Printf("  Target: %s\n", testURL)
	fmt.Println()

	runGoqueryCrawler(ctx, config, sourceInfo, target)

	fmt.Println()

	runChromedpCrawler(ctx, config, sourceInfo, target)

	fmt.Println()
	fmt.Println(divider)
	fmt.Println("  예제 완료")
	fmt.Println(divider)
}

// runGoqueryCrawler: GoqueryCrawler의 메소드 호출 순서를 순차적으로 시연
func runGoqueryCrawler(
	ctx context.Context,
	config core.Config,
	source core.SourceInfo,
	target core.Target,
) {
	fmt.Println(divider)
	fmt.Println("  [GoqueryCrawler] 정적 크롤링 - HTTP + CSS 셀렉터 파싱")
	fmt.Println(divider)

	// ── Step 1: 인스턴스 생성 ──────────────────────────────────────
	printStep(1, "NewGoqueryCrawler(name, source, config)")
	crawler := goquery.NewGoqueryCrawler("goquery-example", source, config)
	fmt.Printf("    name:    %s\n", crawler.Name())
	fmt.Printf("    source:  %s (%s)\n", crawler.Source().Name, crawler.Source().Country)
	fmt.Printf("    baseURL: %s\n", crawler.Source().BaseURL)

	// ── Step 2: Initialize ────────────────────────────────────────
	printStep(2, "Initialize(ctx, config)  →  http.Client 생성")
	if err := crawler.Initialize(ctx, config); err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		return
	}
	fmt.Printf("    http.Client 생성 완료 (timeout: %s)\n", config.Timeout)

	// ── Step 3: Start ─────────────────────────────────────────────
	printStep(3, "Start(ctx)  →  크롤러 활성화")
	if err := crawler.Start(ctx); err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		return
	}
	fmt.Println("    크롤러 시작 완료")

	// ── Step 4: HealthCheck ───────────────────────────────────────
	printStep(4, "HealthCheck(ctx)  →  BaseURL GET 요청으로 연결 확인")
	hStart := time.Now()
	if err := crawler.HealthCheck(ctx); err != nil {
		fmt.Printf("    WARN: %v\n", err)
	} else {
		fmt.Printf("    정상 응답 확인 (%v)\n", time.Since(hStart).Round(time.Millisecond))
	}

	// ── Step 5: Fetch ─────────────────────────────────────────────
	printStep(5, "Fetch(ctx, target)  →  HTTP GET → goquery.Document → RawContent")
	fStart := time.Now()
	raw, err := crawler.Fetch(ctx, target)
	if err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		return
	}
	printRawContent(raw, time.Since(fStart))

	// ── Step 6: FetchAndParse ─────────────────────────────────────
	printStep(6, "FetchAndParse(ctx, target, selectors)  →  HTTP GET → Document → Article")
	fmt.Printf("    selectors: title=%q  body=%q\n", selectors["title"], selectors["body"])
	pStart := time.Now()
	content, err := crawler.FetchAndParse(ctx, target, selectors)
	if err != nil {
		fmt.Printf("    ERROR: %v\n", err)
	} else {
		printContent(content, time.Since(pStart))
	}

	// ── Step 7: Stop ──────────────────────────────────────────────
	printStep(7, "Stop(ctx)  →  크롤러 중지")
	if err := crawler.Stop(ctx); err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		return
	}
	fmt.Println("    중지 완료 (http.Client는 GC 처리)")
}

// runChromedpCrawler: ChromedpCrawler의 메소드 호출 순서를 순차적으로 시연
func runChromedpCrawler(
	ctx context.Context,
	config core.Config,
	source core.SourceInfo,
	target core.Target,
) {
	fmt.Println(divider)
	fmt.Println("  [ChromedpCrawler] 동적 크롤링 - 헤드리스 Chrome + JS 렌더링")
	fmt.Println(divider)

	// ── Step 1: 인스턴스 생성 ──────────────────────────────────────
	printStep(1, "NewChromedpCrawlerWithOptions(name, source, config, opts)")
	// DefaultRemoteOptions: Docker Chrome(chromedp/headless-shell)에 연결
	// docker run -d -p 9222:9222 --name issuetracker-chrome chromedp/headless-shell
	opts := cdp.DefaultRemoteOptions()
	opts.WaitStable = false // 예제 빠른 실행을 위해 2초 대기 비활성화
	crawler := cdp.NewChromedpCrawlerWithOptions("chromedp-example", source, config, opts)
	fmt.Printf("    name:       %s\n", crawler.Name())
	fmt.Printf("    source:     %s (%s)\n", crawler.Source().Name, crawler.Source().Country)
	fmt.Printf("    use_remote: %v\n", opts.UseRemote)
	fmt.Printf("    remote_url: %s\n", opts.RemoteURL)

	// ── Step 2: Initialize ────────────────────────────────────────
	printStep(2, "Initialize(ctx, config)  →  RemoteAllocator 생성 (Docker Chrome 연결)")
	fmt.Printf("    연결 대상: %s\n", opts.RemoteURL)
	if err := crawler.Initialize(ctx, config); err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		return
	}
	fmt.Println("    RemoteAllocator 생성 완료 (CDP WebSocket 연결 준비)")

	// ── Step 3: Start ─────────────────────────────────────────────
	printStep(3, "Start(ctx)  →  크롤러 활성화")
	if err := crawler.Start(ctx); err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		_ = crawler.Stop(ctx)
		return
	}
	fmt.Println("    크롤러 시작 완료")

	// ── Step 4: HealthCheck ───────────────────────────────────────
	printStep(4, "HealthCheck(ctx)  →  about:blank 로드로 Docker Chrome 동작 확인")
	fmt.Println("    (첫 호출: RemoteAllocator → Docker Chrome에 탭 생성 → about:blank 이동)")
	hStart := time.Now()
	if err := crawler.HealthCheck(ctx); err != nil {
		fmt.Printf("    WARN: %v\n", err)
	} else {
		fmt.Printf("    Docker Chrome 정상 동작 확인 (%v)\n", time.Since(hStart).Round(time.Millisecond))
	}

	// ── Step 5: Fetch ─────────────────────────────────────────────
	printStep(5, "Fetch(ctx, target)  →  새 탭 → Navigate → DOM 렌더링 → OuterHTML → RawContent")
	fmt.Println("    내부 순서: NewContext(탭) → buildFetchActions → chromedp.Run(Docker) → OuterHTML")
	fStart := time.Now()
	raw, err := crawler.Fetch(ctx, target)
	if err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		_ = crawler.Stop(ctx)
		return
	}
	printRawContent(raw, time.Since(fStart))

	// ── Step 6: FetchAndParse ─────────────────────────────────────
	printStep(6, "FetchAndParse(ctx, target, selectors)  →  Fetch() + goquery 파싱")
	fmt.Println("    내부 순서: Fetch(렌더링 HTML) → goquery.Document → CSS 셀렉터 추출 → Article")
	fmt.Printf("    selectors: title=%q  body=%q\n", selectors["title"], selectors["body"])
	pStart := time.Now()
	content, err := crawler.FetchAndParse(ctx, target, selectors)
	if err != nil {
		fmt.Printf("    ERROR: %v\n", err)
	} else {
		printContent(content, time.Since(pStart))
	}

	// ── Step 7: EvaluateJS ────────────────────────────────────────
	printStep(7, "EvaluateJS(ctx, url, script)  →  Navigate → JS 실행 → 결과 반환")
	script := "document.title"
	fmt.Printf("    script: %s\n", script)
	jStart := time.Now()
	jsResult, err := crawler.EvaluateJS(ctx, testURL, script)
	if err != nil {
		fmt.Printf("    ERROR: %v\n", err)
	} else {
		fmt.Printf("    JS 실행 결과: %q (%v)\n", jsResult, time.Since(jStart).Round(time.Millisecond))
	}

	// ── Step 8: Stop ──────────────────────────────────────────────
	printStep(8, "Stop(ctx)  →  allocCancel() 호출 → Docker Chrome 연결 해제")
	if err := crawler.Stop(ctx); err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		return
	}
	fmt.Println("    Docker Chrome 연결 해제 완료 (컨테이너는 계속 실행 중)")
}

// printStep: 단계 번호와 메소드명 출력
func printStep(n int, label string) {
	fmt.Printf("\n  Step %d │ %s\n", n, label)
}

// printRawContent: RawContent 주요 필드 출력
func printRawContent(raw *core.RawContent, elapsed time.Duration) {
	preview := strings.ReplaceAll(raw.HTML, "\n", " ")
	if len(preview) > 120 {
		preview = preview[:120] + "..."
	}
	fmt.Printf("    status:   %d\n", raw.StatusCode)
	fmt.Printf("    size:     %d bytes\n", len(raw.HTML))
	fmt.Printf("    duration: %v\n", elapsed.Round(time.Millisecond))
	fmt.Printf("    preview:  %s\n", strings.TrimSpace(preview))
}

// printContent: Content 주요 필드 출력
func printContent(content *core.Content, elapsed time.Duration) {
	bodyPreview := strings.ReplaceAll(content.Body, "\n", " ")
	if len(bodyPreview) > 120 {
		bodyPreview = bodyPreview[:120] + "..."
	}
	fmt.Printf("    duration:  %v\n", elapsed.Round(time.Millisecond))
	fmt.Printf("    title:     %s\n", content.Title)
	fmt.Printf("    author:    %s\n", orEmpty(content.Author))
	fmt.Printf("    words:     %d\n", content.WordCount)
	fmt.Printf("    images:    %d\n", len(content.ImageURLs))
	fmt.Printf("    body:      %s\n", strings.TrimSpace(bodyPreview))
}

// orEmpty: 빈 문자열이면 "(없음)" 반환
func orEmpty(s string) string {
	if s == "" {
		return "(없음)"
	}
	return s
}
