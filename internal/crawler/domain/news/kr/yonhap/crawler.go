package yonhap

import (
  "context"
  "fmt"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/domain/news"
  "issuetracker/pkg/logger"
)

// YonhapCrawler는 연합뉴스 크롤러입니다.
// HTML 파싱(goquery)만을 사용합니다.
// news.NewsCrawler 인터페이스를 구현합니다.
//
// YonhapCrawler implements news.NewsCrawler for Yonhap News Agency.
// Uses HTML scraping via goquery only.
type YonhapCrawler struct {
  config  YonhapConfig
  fetcher news.NewsFetcher
  parser  *YonhapParser
  log     *logger.Logger
}

// NewYonhapCrawler는 새로운 YonhapCrawler를 생성합니다.
func NewYonhapCrawler(
  config  YonhapConfig,
  fetcher news.NewsFetcher,
  parser  *YonhapParser,
  log     *logger.Logger,
) *YonhapCrawler {
  return &YonhapCrawler{
    config:  config,
    fetcher: fetcher,
    parser:  parser,
    log:     log,
  }
}

// Name은 크롤러 이름을 반환합니다.
func (c *YonhapCrawler) Name() string {
  return "yonhap"
}

// Source는 소스 정보를 반환합니다.
func (c *YonhapCrawler) Source() core.SourceInfo {
  return c.config.CrawlerConfig.SourceInfo
}

// Initialize는 크롤러 설정을 업데이트합니다.
func (c *YonhapCrawler) Initialize(_ context.Context, config core.Config) error {
  c.config.CrawlerConfig = config
  return nil
}

// Start는 크롤러를 시작합니다.
func (c *YonhapCrawler) Start(ctx context.Context) error {
  logger.FromContext(ctx).WithField("crawler", c.Name()).Info("yonhap 크롤러 시작")
  return nil
}

// Stop은 크롤러를 중지합니다.
func (c *YonhapCrawler) Stop(ctx context.Context) error {
  logger.FromContext(ctx).WithField("crawler", c.Name()).Info("yonhap 크롤러 중지")
  return nil
}

// HealthCheck는 연합뉴스 메인 페이지에 접근하여 상태를 확인합니다.
func (c *YonhapCrawler) HealthCheck(ctx context.Context) error {
  target := core.Target{
    URL:  c.config.BaseURL,
    Type: core.TargetTypeCategory,
  }
  _, err := c.fetcher.Fetch(ctx, target)
  if err != nil {
    return fmt.Errorf("yonhap health check: %w", err)
  }
  return nil
}

// Fetch는 단일 target에서 RawContent를 가져옵니다.
// core.Crawler 인터페이스 구현용 메서드입니다.
func (c *YonhapCrawler) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
  return c.fetcher.Fetch(ctx, target)
}

// FetchList는 연합뉴스 카테고리 HTML 페이지에서 기사 목록을 가져옵니다.
func (c *YonhapCrawler) FetchList(ctx context.Context, target core.Target) ([]news.NewsItem, error) {
  raw, err := c.fetcher.Fetch(ctx, target)
  if err != nil {
    return nil, fmt.Errorf("yonhap fetch list: %w", err)
  }
  return c.parser.ParseList(raw)
}

// FetchArticle은 단일 기사 URL에서 전체 내용을 가져옵니다.
func (c *YonhapCrawler) FetchArticle(ctx context.Context, url string) (*news.NewsArticle, error) {
  target := core.Target{
    URL:  url,
    Type: core.TargetTypeArticle,
  }

  raw, err := c.fetcher.Fetch(ctx, target)
  if err != nil {
    return nil, fmt.Errorf("yonhap fetch article: %w", err)
  }

  article, err := c.parser.ParseArticle(raw)
  if err != nil {
    return nil, fmt.Errorf("yonhap parse article: %w", err)
  }

  return article, nil
}
