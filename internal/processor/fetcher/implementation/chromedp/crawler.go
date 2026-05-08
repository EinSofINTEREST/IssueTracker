package chromedp

import (
	"context"

	"github.com/chromedp/chromedp"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
)

// NewChromedpCrawler: 새로운 ChromedpCrawler 인스턴스 생성 (rate limiter 없음).
//
// production wiring 은 NewChromedpCrawlerWithRateLimiter 를 사용. 본 생성자는 example /
// test 환경용.
func NewChromedpCrawler(name string, source core.SourceInfo, config core.Config) *ChromedpCrawler {
	return &ChromedpCrawler{
		name:       name,
		sourceInfo: source,
		config:     config,
		opts:       DefaultOptions(),
	}
}

// NewChromedpCrawlerWithOptions: 옵션을 지정하여 인스턴스 생성 (rate limiter 없음).
func NewChromedpCrawlerWithOptions(name string, source core.SourceInfo, config core.Config, opts ChromedpOptions) *ChromedpCrawler {
	return NewChromedpCrawlerWithRateLimiter(name, source, config, opts, nil)
}

// NewChromedpCrawlerWithRateLimiter: rate limiter 가 주입된 ChromedpCrawler 를 생성합니다.
//
// chromedp 는 raw http.Client 를 거치지 않으므로 Fetch entry 에서 직접 Wait 를 호출해
// RPH 정책을 강제합니다. rateLimiter 가 nil 이면 NewChromedpCrawlerWithOptions 와 동일.
func NewChromedpCrawlerWithRateLimiter(name string, source core.SourceInfo, config core.Config, opts ChromedpOptions, rateLimiter core.URLRateLimiter) *ChromedpCrawler {
	return &ChromedpCrawler{
		name:           name,
		sourceInfo:     source,
		config:         config,
		opts:           opts,
		urlRateLimiter: rateLimiter,
	}
}

// Name: 크롤러 이름 반환
func (c *ChromedpCrawler) Name() string {
	return c.name
}

// Source: 소스 정보 반환
func (c *ChromedpCrawler) Source() core.SourceInfo {
	return c.sourceInfo
}

// Initialize: 크롤러 초기화 및 브라우저 할당
// UseRemote=true 이면 원격(Docker) Chrome에 연결, false 이면 로컬 Chrome 프로세스 실행
func (c *ChromedpCrawler) Initialize(ctx context.Context, config core.Config) error {
	log := logger.FromContext(ctx)
	log.WithFields(map[string]interface{}{
		"crawler":    c.name,
		"source":     c.sourceInfo.Name,
		"use_remote": c.opts.UseRemote,
	}).Info("initializing chromedp crawler")

	c.config = config

	if c.opts.UseRemote {
		return c.initRemote(ctx, log)
	}
	return c.initLocal(ctx, log, config)
}

// initRemote: Docker/원격 Chrome에 WebSocket으로 연결
// 원격 Chrome 실행 예시: docker run -d -p 9222:9222 chromedp/headless-shell
func (c *ChromedpCrawler) initRemote(ctx context.Context, log *logger.Logger) error {
	remoteURL := c.opts.RemoteURL
	if remoteURL == "" {
		remoteURL = "ws://localhost:9222"
	}

	log.WithField("remote_url", remoteURL).Info("connecting to remote Chrome (Docker)")

	// NewRemoteAllocator: 이미 실행 중인 Chrome에 CDP WebSocket으로 연결
	c.allocCtx, c.allocCancel = chromedp.NewRemoteAllocator(ctx, remoteURL)

	log.WithField("remote_url", remoteURL).Info("remote Chrome allocator ready")
	return nil
}

// initLocal: 로컬 Chrome 프로세스를 직접 실행
func (c *ChromedpCrawler) initLocal(ctx context.Context, log *logger.Logger, config core.Config) error {
	userAgent := c.opts.UserAgent
	if userAgent == "" {
		userAgent = config.UserAgent
	}

	// 로컬 Chrome 실행 옵션 구성
	chromeOpts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", c.opts.Headless),
		chromedp.UserAgent(userAgent),
		chromedp.WindowSize(int(c.opts.ViewportWidth), int(c.opts.ViewportHeight)),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)

	// ExecAllocator: Chrome 바이너리를 직접 실행하여 관리
	c.allocCtx, c.allocCancel = chromedp.NewExecAllocator(ctx, chromeOpts...)

	log.Info("local Chrome allocator ready")
	return nil
}

// Start: 크롤러 시작
func (c *ChromedpCrawler) Start(ctx context.Context) error {
	log := logger.FromContext(ctx)
	log.WithField("crawler", c.name).Info("chromedp crawler started")
	return nil
}

// Stop: 크롤러 중지 및 브라우저 리소스 해제
func (c *ChromedpCrawler) Stop(ctx context.Context) error {
	log := logger.FromContext(ctx)
	log.WithField("crawler", c.name).Info("chromedp crawler stopping")

	if c.allocCancel != nil {
		c.allocCancel()
	}

	log.WithField("crawler", c.name).Info("chromedp crawler stopped")
	return nil
}

// HealthCheck: 크롤러 상태 확인 (빈 페이지 로드 테스트)
func (c *ChromedpCrawler) HealthCheck(ctx context.Context) error {
	if c.allocCtx == nil {
		return &core.CrawlerError{
			Category: core.ErrCategoryInternal,
			Code:     "CDP_001",
			Message:  "browser not initialized",
			Source:   c.name,
		}
	}

	// 간단한 빈 페이지 로드로 브라우저 동작 확인
	tabCtx, cancel := chromedp.NewContext(c.allocCtx)
	defer cancel()

	return chromedp.Run(tabCtx,
		chromedp.Navigate("about:blank"),
	)
}
