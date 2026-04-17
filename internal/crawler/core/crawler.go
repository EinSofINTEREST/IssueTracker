package core

import "context"

// Crawler는 모든 크롤러가 구현해야 하는 인터페이스입니다.
// 각 소스별 크롤러는 이 인터페이스를 만족해야 합니다.
type Crawler interface {
	// Metadata
	Name() string
	Source() SourceInfo

	// Lifecycle
	Initialize(ctx context.Context, config Config) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error

	// Crawling
	Fetch(ctx context.Context, target Target) (*RawContent, error)

	// Health
	HealthCheck(ctx context.Context) error
}

// Parser는 원본 컨텐츠를 Content로 파싱하는 인터페이스입니다.
type Parser interface {
	Parse(raw *RawContent) (*Content, error)
}

// HTTPClient는 HTTP 요청을 수행하는 인터페이스입니다.
// 테스트를 위해 mock 가능하도록 인터페이스로 정의합니다.
type HTTPClient interface {
	Get(ctx context.Context, url string) (*HTTPResponse, error)
	Post(ctx context.Context, url string, body []byte) (*HTTPResponse, error)
}

// HTTPResponse는 HTTP 응답을 나타냅니다.
type HTTPResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

// RateLimiter는 rate limiting을 수행하는 인터페이스입니다.
type RateLimiter interface {
	// Wait는 rate limit에 따라 대기합니다.
	// context가 cancel되면 즉시 에러를 반환합니다.
	Wait(ctx context.Context) error

	// Allow는 현재 요청이 허용되는지 확인합니다.
	Allow() bool
}

// URLRateLimiter는 URL 기반 rate limiting을 수행하는 인터페이스입니다.
// URL에서 목적지(IP 등)를 해석하여 대상별로 독립된 rate limiting을 적용합니다.
//
// URLRateLimiter performs rate limiting based on the target resolved from a URL.
type URLRateLimiter interface {
	// Wait는 URL의 목적지를 해석하고 해당 대상의 rate limiter에서 대기합니다.
	Wait(ctx context.Context, rawURL string) error
}
