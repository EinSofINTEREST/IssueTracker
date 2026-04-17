package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"issuetracker/pkg/logger"
)

const (
	// MaxResponseBodySize는 응답 body의 최대 크기입니다 (10MB).
	MaxResponseBodySize = 10 * 1024 * 1024
)

// defaultHTTPClient는 기본 HTTP 클라이언트를 생성합니다.
// Connection pooling과 timeout을 적용합니다.
func defaultHTTPClient(config Config) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        config.MaxIdleConns,
		MaxIdleConnsPerHost: config.MaxConnsPerHost,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
		ForceAttemptHTTP2:   true,
	}

	return &http.Client{
		Transport: transport,
		// timeout은 request level에서 적용
		Timeout: 0,
	}
}

// StandardHTTPClient는 표준 HTTP 클라이언트 구현입니다.
type StandardHTTPClient struct {
	client      *http.Client
	userAgent   string
	timeout     time.Duration
	rateLimiter URLRateLimiter
}

// NewHTTPClient는 새로운 HTTP 클라이언트를 생성합니다.
// rate limiter 없이 동작하며, WithRateLimiter로 주입할 수 있습니다.
func NewHTTPClient(config Config) HTTPClient {
	return &StandardHTTPClient{
		client:    defaultHTTPClient(config),
		userAgent: config.UserAgent,
		timeout:   config.Timeout,
	}
}

// NewHTTPClientWithRateLimiter는 URLRateLimiter가 주입된 HTTP 클라이언트를 생성합니다.
// HTTP 요청 전 목적지 기반 rate limiting을 적용합니다.
func NewHTTPClientWithRateLimiter(config Config, rateLimiter URLRateLimiter) HTTPClient {
	return &StandardHTTPClient{
		client:      defaultHTTPClient(config),
		userAgent:   config.UserAgent,
		timeout:     config.Timeout,
		rateLimiter: rateLimiter,
	}
}

// Get은 GET 요청을 수행합니다.
func (c *StandardHTTPClient) Get(ctx context.Context, url string) (*HTTPResponse, error) {
	log := logger.FromContext(ctx)

	// 목적지 기반 rate limiting: rate limiter가 주입된 경우에만 적용
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx, url); err != nil {
			return nil, fmt.Errorf("rate limit wait for %s: %w", url, err)
		}
	}

	start := time.Now()

	log.WithField("url", url).Debug("starting HTTP GET request")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.WithError(err).WithField("url", url).Error("failed to create request")
		return nil, NewNetworkError("REQ_001", "failed to create request", url, err)
	}

	c.setHeaders(req)

	// timeout 적용
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := c.client.Do(req)
	if err != nil {
		elapsed := time.Since(start)
		log.WithError(err).
			WithField("url", url).
			WithField("duration_ms", elapsed.Milliseconds()).
			Error("failed to execute request")
		return nil, NewNetworkError("NET_001", "failed to execute request", url, err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	log.WithFields(map[string]interface{}{
		"url":         url,
		"status":      resp.StatusCode,
		"duration_ms": elapsed.Milliseconds(),
	}).Debug("HTTP GET request completed")

	return c.processResponse(resp, url)
}

// Post는 POST 요청을 수행합니다.
func (c *StandardHTTPClient) Post(ctx context.Context, url string, body []byte) (*HTTPResponse, error) {
	log := logger.FromContext(ctx)

	// 목적지 기반 rate limiting: rate limiter가 주입된 경우에만 적용
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx, url); err != nil {
			return nil, fmt.Errorf("rate limit wait for %s: %w", url, err)
		}
	}

	start := time.Now()

	log.WithFields(map[string]interface{}{
		"url":       url,
		"body_size": len(body),
	}).Debug("starting HTTP POST request")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.WithError(err).WithField("url", url).Error("failed to create request")
		return nil, NewNetworkError("REQ_001", "failed to create request", url, err)
	}

	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	// timeout 적용
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := c.client.Do(req)
	if err != nil {
		elapsed := time.Since(start)
		log.WithError(err).
			WithField("url", url).
			WithField("duration_ms", elapsed.Milliseconds()).
			Error("failed to execute request")
		return nil, NewNetworkError("NET_001", "failed to execute request", url, err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	log.WithFields(map[string]interface{}{
		"url":         url,
		"status":      resp.StatusCode,
		"duration_ms": elapsed.Milliseconds(),
	}).Debug("HTTP POST request completed")

	return c.processResponse(resp, url)
}

// setHeaders는 요청에 공통 헤더를 설정합니다.
func (c *StandardHTTPClient) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
}

// processResponse는 HTTP 응답을 처리합니다.
func (c *StandardHTTPClient) processResponse(resp *http.Response, url string) (*HTTPResponse, error) {
	// Status code 체크
	if resp.StatusCode == http.StatusNotFound {
		return nil, NewNotFoundError(url)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, NewRateLimitError("HTTP_429", "rate limited", url, resp.StatusCode)
	}

	if resp.StatusCode >= 500 {
		return nil, NewNetworkError(
			fmt.Sprintf("HTTP_%d", resp.StatusCode),
			"server error",
			url,
			fmt.Errorf("status code: %d", resp.StatusCode),
		)
	}

	if resp.StatusCode >= 400 {
		return nil, &CrawlerError{
			Category:   ErrCategoryInternal,
			Code:       fmt.Sprintf("HTTP_%d", resp.StatusCode),
			Message:    "client error",
			URL:        url,
			StatusCode: resp.StatusCode,
			Retryable:  false,
		}
	}

	// Body 읽기 (크기 제한 적용)
	limitedReader := io.LimitReader(resp.Body, MaxResponseBodySize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, NewNetworkError("NET_002", "failed to read response body", url, err)
	}

	// Headers 추출
	headers := make(map[string]string)
	for key, values := range resp.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	return &HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       body,
	}, nil
}
