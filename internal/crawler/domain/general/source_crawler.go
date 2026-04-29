package general

import (
	"context"
	"fmt"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// GenericCrawler 는 모든 사이트가 공유하는 단일 SourceCrawler 구현체입니다.
//
// GenericCrawler is the single SourceCrawler implementation shared by all sites.
// 기존 NaverCrawler/DaumCrawler/YonhapCrawler/CNNCrawler 의 중복 보일러플레이트 (~120줄 × 4)
// 를 본 한 타입으로 통합 — 사이트는 SourceInfo 와 baseURL 만 다름.
//
// 본 타입은 fetcher 를 위임자로 들고 core.Crawler 라이프사이클을 만족하는 얇은 shell.
// 실제 페이지 파싱은 ChainHandler 가 rule.Parser 를 통해 수행하므로 사이트별 코드 불필요.
type GenericCrawler struct {
	name    string
	source  core.SourceInfo
	fetcher Fetcher
	baseURL string
	config  core.Config
}

// NewGenericCrawler 는 신규 GenericCrawler 를 생성합니다.
//
//   - name: registry 등록 이름 (예: "naver", "cnn")
//   - source: SourceInfo (country / type / name / baseURL / language)
//   - fetcher: HealthCheck 시 사용할 fetcher (보통 GoQuery)
//   - baseURL: HealthCheck 대상
//   - config: 초기 core.Config
func NewGenericCrawler(name string, source core.SourceInfo, fetcher Fetcher, baseURL string, config core.Config) *GenericCrawler {
	return &GenericCrawler{
		name:    name,
		source:  source,
		fetcher: fetcher,
		baseURL: baseURL,
		config:  config,
	}
}

// Name 은 크롤러 이름을 반환합니다.
func (c *GenericCrawler) Name() string { return c.name }

// Source 는 소스 정보를 반환합니다.
func (c *GenericCrawler) Source() core.SourceInfo { return c.source }

// Initialize 는 크롤러 설정을 갱신합니다.
func (c *GenericCrawler) Initialize(_ context.Context, config core.Config) error {
	c.config = config
	return nil
}

// Start 는 크롤러를 시작합니다 (현재는 로그만 — 라이프사이클 hook).
func (c *GenericCrawler) Start(ctx context.Context) error {
	logger.FromContext(ctx).WithField("crawler", c.name).Info("crawler started")
	return nil
}

// Stop 은 크롤러를 정지합니다.
func (c *GenericCrawler) Stop(ctx context.Context) error {
	logger.FromContext(ctx).WithField("crawler", c.name).Info("crawler stopped")
	return nil
}

// HealthCheck 는 baseURL 에 접근하여 가용성을 확인합니다.
func (c *GenericCrawler) HealthCheck(ctx context.Context) error {
	target := core.Target{URL: c.baseURL, Type: core.TargetTypeCategory}
	if _, err := c.fetcher.Fetch(ctx, target); err != nil {
		return fmt.Errorf("%s health check: %w", c.name, err)
	}
	return nil
}

// Fetch 는 단일 target 의 RawContent 를 반환합니다 (core.Crawler 구현용 — 실제 파이프라인은
// ChainHandler 경로 사용).
func (c *GenericCrawler) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	return c.fetcher.Fetch(ctx, target)
}

// 컴파일 시 인터페이스 충족 검증.
var _ SourceCrawler = (*GenericCrawler)(nil)
