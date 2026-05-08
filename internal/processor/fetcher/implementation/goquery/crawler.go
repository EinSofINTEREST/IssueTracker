package goquery

import (
	"context"
	"fmt"
	"net/http"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
)

// NewGoqueryCrawler: 새로운 GoqueryCrawler 인스턴스 생성 (rate limiter 없음).
//
// production wiring 은 NewGoqueryCrawlerWithRateLimiter 를 사용하여 사이트별 RPH 정책을
// 강제. 본 생성자는 example / test / 테스트 환경에서 사용.
func NewGoqueryCrawler(name string, source core.SourceInfo, config core.Config) *GoqueryCrawler {
	return NewGoqueryCrawlerWithRateLimiter(name, source, config, nil)
}

// NewGoqueryCrawlerWithRateLimiter: rate limiter 가 주입된 GoqueryCrawler 를 생성합니다.
//
// rateLimiter 가 nil 이면 NewGoqueryCrawler 와 동일 (제한 없음). 비-nil 이면 매 fetch 직전
// rateLimiter.Wait(ctx, url) 호출하여 RPH 정책 강제. ctx cancel 또는 timeout 시 에러 반환.
func NewGoqueryCrawlerWithRateLimiter(name string, source core.SourceInfo, config core.Config, rateLimiter core.URLRateLimiter) *GoqueryCrawler {
	return &GoqueryCrawler{
		name:       name,
		sourceInfo: source,
		config:     config,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		urlRateLimiter: rateLimiter,
	}
}

// Name: 크롤러 이름 반환
func (c *GoqueryCrawler) Name() string {
	return c.name
}

// Source: 소스 정보 반환
func (c *GoqueryCrawler) Source() core.SourceInfo {
	return c.sourceInfo
}

// Initialize: 크롤러 초기화
func (c *GoqueryCrawler) Initialize(ctx context.Context, config core.Config) error {
	log := logger.FromContext(ctx)
	log.WithFields(map[string]interface{}{
		"crawler": c.name,
		"source":  c.sourceInfo.Name,
	}).Info("initializing goquery crawler")

	c.config = config
	c.httpClient = &http.Client{
		Timeout: config.Timeout,
	}

	return nil
}

// Start: 크롤러 시작
func (c *GoqueryCrawler) Start(ctx context.Context) error {
	log := logger.FromContext(ctx)
	log.WithField("crawler", c.name).Info("goquery crawler started")
	return nil
}

// Stop: 크롤러 중지
func (c *GoqueryCrawler) Stop(ctx context.Context) error {
	log := logger.FromContext(ctx)
	log.WithField("crawler", c.name).Info("goquery crawler stopped")
	return nil
}

// HealthCheck: 크롤러 상태 확인
func (c *GoqueryCrawler) HealthCheck(ctx context.Context) error {
	if c.sourceInfo.BaseURL == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", c.sourceInfo.BaseURL, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("health check failed with status %d", resp.StatusCode)
	}

	return nil
}
