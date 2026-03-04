package naver

import (
	"context"
	"fmt"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/pkg/logger"
)

// NaverCrawler는 네이버 뉴스 크롤러입니다.
// news.NewsCrawler 인터페이스를 구현하며, DIP에 따라 news.NewsFetcher 인터페이스에만 의존합니다.
//
// NaverCrawler implements news.NewsCrawler for Naver News.
// It depends on the news.NewsFetcher interface (DIP), not on GoqueryCrawler directly.
type NaverCrawler struct {
	config  NaverConfig
	fetcher news.NewsFetcher
	parser  *NaverParser
	log     *logger.Logger
}

// NewNaverCrawler는 새로운 NaverCrawler를 생성합니다.
func NewNaverCrawler(
	config NaverConfig,
	fetcher news.NewsFetcher,
	parser *NaverParser,
	log *logger.Logger,
) *NaverCrawler {
	return &NaverCrawler{
		config:  config,
		fetcher: fetcher,
		parser:  parser,
		log:     log,
	}
}

// Name은 크롤러 이름을 반환합니다.
func (c *NaverCrawler) Name() string {
	return "naver"
}

// Source는 소스 정보를 반환합니다.
func (c *NaverCrawler) Source() core.SourceInfo {
	return c.config.CrawlerConfig.SourceInfo
}

// Initialize는 크롤러 설정을 업데이트합니다.
func (c *NaverCrawler) Initialize(_ context.Context, config core.Config) error {
	c.config.CrawlerConfig = config
	return nil
}

// Start는 크롤러를 시작합니다.
func (c *NaverCrawler) Start(ctx context.Context) error {
	logger.FromContext(ctx).WithField("crawler", c.Name()).Info("naver 크롤러 시작")
	return nil
}

// Stop은 크롤러를 중지합니다.
func (c *NaverCrawler) Stop(ctx context.Context) error {
	logger.FromContext(ctx).WithField("crawler", c.Name()).Info("naver 크롤러 중지")
	return nil
}

// HealthCheck는 네이버 뉴스 메인 페이지에 접근하여 상태를 확인합니다.
func (c *NaverCrawler) HealthCheck(ctx context.Context) error {
	target := core.Target{
		URL:  c.config.BaseURL,
		Type: core.TargetTypeCategory,
	}
	_, err := c.fetcher.Fetch(ctx, target)
	if err != nil {
		return fmt.Errorf("naver health check: %w", err)
	}
	return nil
}

// Fetch는 단일 target에서 RawContent를 가져옵니다.
// core.Crawler 인터페이스 구현용 메서드입니다.
func (c *NaverCrawler) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	return c.fetcher.Fetch(ctx, target)
}

// FetchList는 네이버 뉴스 카테고리 페이지에서 기사 목록을 가져옵니다.
func (c *NaverCrawler) FetchList(ctx context.Context, target core.Target) ([]news.NewsItem, error) {
	raw, err := c.fetcher.Fetch(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("naver fetch list: %w", err)
	}

	items, err := c.parser.ParseList(raw)
	if err != nil {
		return nil, fmt.Errorf("naver parse list: %w", err)
	}

	return items, nil
}

// FetchArticle은 단일 기사 URL에서 전체 내용을 가져옵니다.
func (c *NaverCrawler) FetchArticle(ctx context.Context, url string) (*news.NewsArticle, error) {
	target := core.Target{
		URL:  url,
		Type: core.TargetTypeArticle,
	}

	raw, err := c.fetcher.Fetch(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("naver fetch article: %w", err)
	}

	article, err := c.parser.ParseArticle(raw)
	if err != nil {
		return nil, fmt.Errorf("naver parse article: %w", err)
	}

	return article, nil
}
