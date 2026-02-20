package main

import (
  "context"
  "fmt"
  "time"

  "ecoscrapper/internal/crawler/core"
  cdp "ecoscrapper/internal/crawler/implementation/chromedp"
  "ecoscrapper/internal/crawler/implementation/goquery"
  "ecoscrapper/pkg/logger"
)

func main() {
  log := logger.New(logger.Config{
    Level:  logger.LevelInfo,
    Pretty: true,
  })
  ctx := log.ToContext(context.Background())

  config := core.Config{
    UserAgent:       "EcoScrapper/1.0 (+https://example.com/bot)",
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
    URL: "https://httpbin.org/html",
  }

  fmt.Println("=== Crawler Comparison ===")
  fmt.Println()

  // 1. Goquery (정적 크롤링)
  fmt.Println("[1] Goquery - Static Crawling")
  gCrawler := goquery.NewGoqueryCrawler("goquery", sourceInfo, config)
  if err := gCrawler.Initialize(ctx, config); err != nil {
    log.Errorf("goquery init failed: %v", err)
  } else {
    runCrawler(ctx, gCrawler, target)
  }

  fmt.Println()

  // 2. Chromedp (동적 크롤링)
  fmt.Println("[2] Chromedp - Dynamic Crawling (headless browser)")
  cCrawler := cdp.NewChromedpCrawler("chromedp", sourceInfo, config)
  if err := cCrawler.Initialize(ctx, config); err != nil {
    log.Errorf("chromedp init failed: %v", err)
  } else {
    runCrawler(ctx, cCrawler, target)
    if err := cCrawler.Stop(ctx); err != nil {
      log.Errorf("chromedp stop failed: %v", err)
    }
  }

  fmt.Println()
  fmt.Println("=== Comparison Complete ===")
}

func runCrawler(ctx context.Context, crawler core.Crawler, target core.Target) {
  start := time.Now()

  rawContent, err := crawler.Fetch(ctx, target)
  if err != nil {
    fmt.Printf("  Fetch failed: %v\n", err)
    return
  }

  elapsed := time.Since(start)

  fmt.Printf("  Fetch successful\n")
  fmt.Printf("    Duration:     %v\n", elapsed)
  fmt.Printf("    Status:       %d\n", rawContent.StatusCode)
  fmt.Printf("    Content size: %d bytes\n", len(rawContent.HTML))
  fmt.Printf("    URL:          %s\n", rawContent.URL)
}
