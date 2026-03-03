package daum_test

import (
  "context"
  "errors"
  "io"
  "testing"

  "github.com/stretchr/testify/assert"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/domain/news"
  "issuetracker/internal/crawler/domain/news/kr/daum"
  "issuetracker/pkg/logger"
)

// mockFetcher는 news.NewsFetcher의 테스트용 구현입니다.
type mockFetcher struct {
  fetchFn func(ctx context.Context, target core.Target) (*core.RawContent, error)
}

func (m *mockFetcher) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
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

func TestDaumCrawler_FetchArticle_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  log := newTestLogger()

  html := `<html><body>
    <h3 class="tit_view">다음 기사 제목</h3>
    <div class="article_view"><p>다음 기사 본문입니다.</p></div>
  </body></html>`

  fetcher := &mockFetcher{
    fetchFn: func(_ context.Context, target core.Target) (*core.RawContent, error) {
      assert.Equal(t, core.TargetTypeArticle, target.Type)
      return newTestRawContent(target.URL, html), nil
    },
  }

  parser := daum.NewDaumParser(cfg)
  crawler := daum.NewDaumCrawler(cfg, fetcher, parser, log)

  article, err := crawler.FetchArticle(context.Background(), "https://v.daum.net/v/test001")

  assert.NoError(t, err)
  assert.Equal(t, "다음 기사 제목", article.Title)
  assert.Contains(t, article.Body, "다음 기사 본문입니다")
}

func TestDaumCrawler_FetchArticle_fetch실패_오류반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  log := newTestLogger()

  fetchErr := core.NewNetworkError("NET_001", "connection refused", "https://v.daum.net", errors.New("dial error"))

  fetcher := &mockFetcher{
    fetchFn: func(_ context.Context, _ core.Target) (*core.RawContent, error) {
      return nil, fetchErr
    },
  }

  parser := daum.NewDaumParser(cfg)
  crawler := daum.NewDaumCrawler(cfg, fetcher, parser, log)

  article, err := crawler.FetchArticle(context.Background(), "https://v.daum.net/v/test002")

  assert.Nil(t, article)
  assert.Error(t, err)
}

func TestDaumCrawler_FetchArticle_파싱실패_오류반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  log := newTestLogger()

  // 제목 없는 HTML → PARSE_002
  html := `<html><body><div class="article_view"><p>본문만.</p></div></body></html>`

  fetcher := &mockFetcher{
    fetchFn: func(_ context.Context, target core.Target) (*core.RawContent, error) {
      return newTestRawContent(target.URL, html), nil
    },
  }

  parser := daum.NewDaumParser(cfg)
  crawler := daum.NewDaumCrawler(cfg, fetcher, parser, log)

  article, err := crawler.FetchArticle(context.Background(), "https://v.daum.net/v/test003")

  assert.Nil(t, article)
  assert.Error(t, err)
}

func TestDaumCrawler_FetchList_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  log := newTestLogger()

  html := `<html><body>
    <div class="item_issue">
      <a class="link_txt" href="https://v.daum.net/v/article1">기사1</a>
    </div>
    <div class="item_issue">
      <a class="link_txt" href="https://v.daum.net/v/article2">기사2</a>
    </div>
  </body></html>`

  fetcher := &mockFetcher{
    fetchFn: func(_ context.Context, target core.Target) (*core.RawContent, error) {
      assert.Equal(t, core.TargetTypeCategory, target.Type)
      return newTestRawContent(target.URL, html), nil
    },
  }

  parser := daum.NewDaumParser(cfg)
  crawler := daum.NewDaumCrawler(cfg, fetcher, parser, log)

  target := core.Target{
    URL:  "https://news.daum.net/politics",
    Type: core.TargetTypeCategory,
  }

  items, err := crawler.FetchList(context.Background(), target)

  assert.NoError(t, err)
  assert.Len(t, items, 2)
  assert.Equal(t, "https://v.daum.net/v/article1", items[0].URL)
  assert.Equal(t, "기사1", items[0].Title)
}

func TestDaumCrawler_FetchList_fetch실패_오류반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  log := newTestLogger()

  fetcher := &mockFetcher{
    fetchFn: func(_ context.Context, _ core.Target) (*core.RawContent, error) {
      return nil, errors.New("network error")
    },
  }

  parser := daum.NewDaumParser(cfg)
  crawler := daum.NewDaumCrawler(cfg, fetcher, parser, log)

  target := core.Target{
    URL:  "https://news.daum.net/politics",
    Type: core.TargetTypeCategory,
  }

  items, err := crawler.FetchList(context.Background(), target)

  assert.Nil(t, items)
  assert.Error(t, err)
}

func TestDaumCrawler_Name_반환값(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  crawler := daum.NewDaumCrawler(cfg, &mockFetcher{}, daum.NewDaumParser(cfg), newTestLogger())

  assert.Equal(t, "daum", crawler.Name())
}

func TestDaumCrawler_Source_소스정보반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  crawler := daum.NewDaumCrawler(cfg, &mockFetcher{}, daum.NewDaumParser(cfg), newTestLogger())

  source := crawler.Source()

  assert.Equal(t, "KR", source.Country)
  assert.Equal(t, core.SourceTypeNews, source.Type)
  assert.Equal(t, "daum", source.Name)
}

func TestDaumCrawler_Initialize_설정갱신(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  crawler := daum.NewDaumCrawler(cfg, &mockFetcher{}, daum.NewDaumParser(cfg), newTestLogger())

  newConfig := core.DefaultConfig()
  newConfig.RequestsPerHour = 50

  err := crawler.Initialize(context.Background(), newConfig)

  assert.NoError(t, err)
}

// mockFetcher가 news.NewsFetcher를 구현하는지 컴파일 타임 검증
var _ news.NewsFetcher = (*mockFetcher)(nil)
