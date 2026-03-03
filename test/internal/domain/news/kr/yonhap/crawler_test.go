package yonhap_test

import (
  "context"
  "errors"
  "io"
  "testing"
  "time"

  "github.com/stretchr/testify/assert"
  "github.com/stretchr/testify/require"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/domain/news"
  "issuetracker/internal/crawler/domain/news/kr/yonhap"
  "issuetracker/pkg/logger"
)

// mockFetcher는 news.NewsFetcher의 테스트용 구현입니다.
type mockFetcher struct {
  fetchFn func(ctx context.Context, target core.Target) (*core.RawContent, error)
  called  bool
}

func (m *mockFetcher) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
  m.called = true
  return m.fetchFn(ctx, target)
}

func newTestLogger() *logger.Logger {
  return logger.New(logger.Config{
    Level:  logger.LevelError,
    Output: io.Discard,
  })
}

func newTestRawContent(url, html string) *core.RawContent {
  return &core.RawContent{
    URL:        url,
    HTML:       html,
    StatusCode: 200,
  }
}

func TestYonhapCrawler_FetchList_성공(t *testing.T) {
  cfg := yonhap.DefaultYonhapConfig()
  log := newTestLogger()

  html := `<html><body>
    <div class="alist-item">
      <a href="https://www.yna.co.kr/view/AKR20240115000000001">
        <span class="alist-item-txt">첫 번째 기사</span>
      </a>
    </div>
    <div class="alist-item">
      <a href="https://www.yna.co.kr/view/AKR20240115000000002">
        <span class="alist-item-txt">두 번째 기사</span>
      </a>
    </div>
  </body></html>`

  htmlFetcher := &mockFetcher{
    fetchFn: func(_ context.Context, target core.Target) (*core.RawContent, error) {
      assert.Equal(t, core.TargetTypeCategory, target.Type)
      return newTestRawContent(target.URL, html), nil
    },
  }

  parser := yonhap.NewYonhapParser(cfg)
  crawler := yonhap.NewYonhapCrawler(cfg, htmlFetcher, parser, log)

  target := core.Target{
    URL:  "https://www.yna.co.kr/politics/all",
    Type: core.TargetTypeCategory,
  }

  items, err := crawler.FetchList(context.Background(), target)

  assert.NoError(t, err)
  assert.Len(t, items, 2)
  assert.Equal(t, "https://www.yna.co.kr/view/AKR20240115000000001", items[0].URL)
  assert.Equal(t, "첫 번째 기사", items[0].Title)
  assert.Equal(t, "https://www.yna.co.kr/view/AKR20240115000000002", items[1].URL)
  assert.True(t, htmlFetcher.called)
}

func TestYonhapCrawler_FetchList_fetch실패_오류반환(t *testing.T) {
  cfg := yonhap.DefaultYonhapConfig()
  log := newTestLogger()

  htmlFetcher := &mockFetcher{
    fetchFn: func(_ context.Context, _ core.Target) (*core.RawContent, error) {
      return nil, errors.New("html fetch error")
    },
  }

  parser := yonhap.NewYonhapParser(cfg)
  crawler := yonhap.NewYonhapCrawler(cfg, htmlFetcher, parser, log)

  target := core.Target{
    URL:  "https://www.yna.co.kr/politics/all",
    Type: core.TargetTypeCategory,
  }

  items, err := crawler.FetchList(context.Background(), target)

  assert.Nil(t, items)
  assert.Error(t, err)
}

func TestYonhapCrawler_FetchArticle_성공(t *testing.T) {
  cfg := yonhap.DefaultYonhapConfig()
  log := newTestLogger()

  html := `<html><body>
    <h1 class="tit01">연합뉴스 기사 제목</h1>
    <div class="story-news article"><p>기사 본문입니다.</p></div>
    <div id="newsWriterCarousel01" class="writer-zone01">
      <div><div><div><div><strong>박연합 기자</strong></div></div></div></div>
    </div>
    <div class="update-time" data-published-time="2024-01-15 14:30"></div>
  </body></html>`

  htmlFetcher := &mockFetcher{
    fetchFn: func(_ context.Context, target core.Target) (*core.RawContent, error) {
      assert.Equal(t, core.TargetTypeArticle, target.Type)
      return newTestRawContent(target.URL, html), nil
    },
  }

  parser := yonhap.NewYonhapParser(cfg)
  crawler := yonhap.NewYonhapCrawler(cfg, htmlFetcher, parser, log)

  article, err := crawler.FetchArticle(context.Background(), "https://www.yna.co.kr/view/AKR20240115000000001")

  require.NoError(t, err)
  require.NotNil(t, article)
  assert.Equal(t, "연합뉴스 기사 제목", article.Title)
  assert.Contains(t, article.Body, "기사 본문입니다")
  // KST 2024-01-15 14:30을 UTC 2024-01-15 05:30으로 변환 (KST는 UTC+9)
  expectedUTC := time.Date(2024, 1, 15, 5, 30, 0, 0, time.UTC)
  assert.Equal(t, expectedUTC, article.PublishedAt)
}

func TestYonhapCrawler_FetchArticle_fetch실패_오류반환(t *testing.T) {
  cfg := yonhap.DefaultYonhapConfig()
  log := newTestLogger()

  fetchErr := core.NewNetworkError("NET_001", "connection refused", "https://www.yna.co.kr", errors.New("dial error"))

  htmlFetcher := &mockFetcher{
    fetchFn: func(_ context.Context, _ core.Target) (*core.RawContent, error) {
      return nil, fetchErr
    },
  }

  parser := yonhap.NewYonhapParser(cfg)
  crawler := yonhap.NewYonhapCrawler(cfg, htmlFetcher, parser, log)

  article, err := crawler.FetchArticle(context.Background(), "https://www.yna.co.kr/view/AKR20240115000000001")

  assert.Nil(t, article)
  assert.Error(t, err)
}

func TestYonhapCrawler_Name_반환값(t *testing.T) {
  cfg := yonhap.DefaultYonhapConfig()
  crawler := yonhap.NewYonhapCrawler(cfg, &mockFetcher{}, yonhap.NewYonhapParser(cfg), newTestLogger())

  assert.Equal(t, "yonhap", crawler.Name())
}

func TestYonhapCrawler_Source_소스정보반환(t *testing.T) {
  cfg := yonhap.DefaultYonhapConfig()
  cfg.CrawlerConfig.SourceInfo = core.SourceInfo{
    Country: "KR",
    Type:    core.SourceTypeNews,
    Name:    "yonhap",
  }

  crawler := yonhap.NewYonhapCrawler(cfg, &mockFetcher{}, yonhap.NewYonhapParser(cfg), newTestLogger())

  source := crawler.Source()

  assert.Equal(t, "KR", source.Country)
  assert.Equal(t, core.SourceTypeNews, source.Type)
  assert.Equal(t, "yonhap", source.Name)
}

func TestYonhapCrawler_Initialize_설정갱신(t *testing.T) {
  cfg := yonhap.DefaultYonhapConfig()
  crawler := yonhap.NewYonhapCrawler(cfg, &mockFetcher{}, yonhap.NewYonhapParser(cfg), newTestLogger())

  newConfig := core.DefaultConfig()
  newConfig.RequestsPerHour = 50

  err := crawler.Initialize(context.Background(), newConfig)

  assert.NoError(t, err)
}

// 컴파일 타임 인터페이스 구현 검증
var _ news.NewsFetcher = (*mockFetcher)(nil)
