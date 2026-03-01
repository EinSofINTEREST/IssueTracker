package goquery

import (
	"context"
	"fmt"
	"net/http"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// NewGoqueryCrawler: 새로운 GoqueryCrawler 인스턴스 생성
func NewGoqueryCrawler(name string, source core.SourceInfo, config core.Config) *GoqueryCrawler {
	return &GoqueryCrawler{
		name:       name,
		sourceInfo: source,
		config:     config,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
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
