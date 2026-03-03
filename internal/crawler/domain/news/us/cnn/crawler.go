package cnn

import (
  "context"
  "fmt"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/domain/news"
  "issuetracker/pkg/logger"
)

// CNNCrawler는 CNN 뉴스 크롤러입니다.
// RSS 피드와 HTML 파싱(goquery) 전략을 사용합니다.
// news.NewsCrawler 인터페이스를 구현합니다.
//
// CNNCrawler implements news.NewsCrawler for CNN.
// It uses RSS feeds for article lists and HTML scraping for full content.
type CNNCrawler struct {
  config  CNNConfig
  fetcher news.NewsFetcher
  parser  *CNNParser
  log     *logger.Logger
}

// NewCNNCrawler는 새로운 CNNCrawler를 생성합니다.
func NewCNNCrawler(
  config CNNConfig,
  fetcher news.NewsFetcher,
  parser *CNNParser,
  log *logger.Logger,
) *CNNCrawler {
  return &CNNCrawler{
    config:  config,
    fetcher: fetcher,
    parser:  parser,
    log:     log,
  }
}

// Name은 크롤러 이름을 반환합니다.
func (c *CNNCrawler) Name() string {
  return "cnn"
}

// Source는 소스 정보를 반환합니다.
func (c *CNNCrawler) Source() core.SourceInfo {
  return c.config.CrawlerConfig.SourceInfo
}

// Initialize는 크롤러 설정을 업데이트합니다.
func (c *CNNCrawler) Initialize(_ context.Context, config core.Config) error {
  c.config.CrawlerConfig = config
  return nil
}

// Start는 크롤러를 시작합니다.
func (c *CNNCrawler) Start(ctx context.Context) error {
  logger.FromContext(ctx).WithField("crawler", c.Name()).Info("cnn 크롤러 시작")
  return nil
}

// Stop은 크롤러를 중지합니다.
func (c *CNNCrawler) Stop(ctx context.Context) error {
  logger.FromContext(ctx).WithField("crawler", c.Name()).Info("cnn 크롤러 중지")
  return nil
}

// HealthCheck는 CNN 메인 페이지 접근으로 상태를 확인합니다.
func (c *CNNCrawler) HealthCheck(ctx context.Context) error {
  target := core.Target{
    URL:  c.config.BaseURL,
    Type: core.TargetTypeCategory,
  }
  _, err := c.fetcher.Fetch(ctx, target)
  if err != nil {
    return fmt.Errorf("cnn health check: %w", err)
  }
  return nil
}

// Fetch는 단일 target에서 RawContent를 가져옵니다.
// core.Crawler 인터페이스 구현용 메서드입니다.
func (c *CNNCrawler) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
  return c.fetcher.Fetch(ctx, target)
}

// FetchList는 CNN 카테고리 HTML 페이지에서 기사 목록을 가져옵니다.
func (c *CNNCrawler) FetchList(ctx context.Context, target core.Target) ([]news.NewsItem, error) {
  raw, err := c.fetcher.Fetch(ctx, target)
  if err != nil {
    return nil, fmt.Errorf("cnn fetch list: %w", err)
  }
  return c.parser.ParseList(raw)
}

// FetchArticle은 단일 기사 URL에서 전체 내용을 가져옵니다.
func (c *CNNCrawler) FetchArticle(ctx context.Context, url string) (*news.NewsArticle, error) {
  target := core.Target{
    URL:  url,
    Type: core.TargetTypeArticle,
  }

  raw, err := c.fetcher.Fetch(ctx, target)
  if err != nil {
    return nil, fmt.Errorf("cnn fetch article: %w", err)
  }

  article, err := c.parser.ParseArticle(raw)
  if err != nil {
    return nil, fmt.Errorf("cnn parse article: %w", err)
  }

  return article, nil
}
