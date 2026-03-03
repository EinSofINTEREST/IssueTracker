package naver_test

import (
  "context"
  "errors"
  "io"
  "testing"

  "github.com/stretchr/testify/assert"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/domain/news"
  "issuetracker/internal/crawler/domain/news/kr/naver"
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

func TestNaverCrawler_FetchArticle_성공(t *testing.T) {
  cfg := naver.DefaultNaverConfig()
  log := newTestLogger()

  html := `<html><body>
    <div id="title_area"><span>기사 제목</span></div>
    <div id="dic_area"><p>기사 본문입니다.</p></div>
  </body></html>`

  fetcher := &mockFetcher{
    fetchFn: func(_ context.Context, target core.Target) (*core.RawContent, error) {
      assert.Equal(t, core.TargetTypeArticle, target.Type)
      return newTestRawContent(target.URL, html), nil
    },
  }

  parser := naver.NewNaverParser(cfg)
  crawler := naver.NewNaverCrawler(cfg, fetcher, parser, log)

  article, err := crawler.FetchArticle(context.Background(), "https://n.news.naver.com/article/001/test")

  assert.NoError(t, err)
  assert.Equal(t, "기사 제목", article.Title)
  assert.Contains(t, article.Body, "기사 본문입니다")
}

func TestNaverCrawler_FetchArticle_fetch실패_오류반환(t *testing.T) {
  cfg := naver.DefaultNaverConfig()
  log := newTestLogger()

  fetchErr := core.NewNetworkError("NET_001", "connection refused", "https://n.news.naver.com", errors.New("dial error"))

  fetcher := &mockFetcher{
    fetchFn: func(_ context.Context, _ core.Target) (*core.RawContent, error) {
      return nil, fetchErr
    },
  }

  parser := naver.NewNaverParser(cfg)
  crawler := naver.NewNaverCrawler(cfg, fetcher, parser, log)

  article, err := crawler.FetchArticle(context.Background(), "https://n.news.naver.com/article/001/test")

  assert.Nil(t, article)
  assert.Error(t, err)
}

func TestNaverCrawler_FetchList_성공(t *testing.T) {
  cfg := naver.DefaultNaverConfig()
  log := newTestLogger()

  html := `<html><body>
    <div class="sa_item">
      <a class="sa_text_title" href="https://news.naver.com/article/1">기사1</a>
    </div>
    <div class="sa_item">
      <a class="sa_text_title" href="https://news.naver.com/article/2">기사2</a>
    </div>
  </body></html>`

  fetcher := &mockFetcher{
    fetchFn: func(_ context.Context, target core.Target) (*core.RawContent, error) {
      assert.Equal(t, core.TargetTypeCategory, target.Type)
      return newTestRawContent(target.URL, html), nil
    },
  }

  parser := naver.NewNaverParser(cfg)
  crawler := naver.NewNaverCrawler(cfg, fetcher, parser, log)

  target := core.Target{
    URL:  "https://news.naver.com/section/100",
    Type: core.TargetTypeCategory,
  }

  items, err := crawler.FetchList(context.Background(), target)

  assert.NoError(t, err)
  assert.Len(t, items, 2)
  assert.Equal(t, "https://news.naver.com/article/1", items[0].URL)
}

func TestNaverCrawler_Name_반환값(t *testing.T) {
  cfg := naver.DefaultNaverConfig()
  crawler := naver.NewNaverCrawler(cfg, &mockFetcher{}, naver.NewNaverParser(cfg), newTestLogger())

  assert.Equal(t, "naver", crawler.Name())
}

func TestNaverCrawler_Source_소스정보반환(t *testing.T) {
  cfg := naver.DefaultNaverConfig()
  crawler := naver.NewNaverCrawler(cfg, &mockFetcher{}, naver.NewNaverParser(cfg), newTestLogger())

  source := crawler.Source()

  assert.Equal(t, "KR", source.Country)
  assert.Equal(t, core.SourceTypeNews, source.Type)
  assert.Equal(t, "naver", source.Name)
}

func TestNaverCrawler_Initialize_설정갱신(t *testing.T) {
  cfg := naver.DefaultNaverConfig()
  crawler := naver.NewNaverCrawler(cfg, &mockFetcher{}, naver.NewNaverParser(cfg), newTestLogger())

  newConfig := core.DefaultConfig()
  newConfig.RequestsPerHour = 50

  err := crawler.Initialize(context.Background(), newConfig)

  assert.NoError(t, err)
}

// mockFetcher가 news.NewsFetcher를 구현하는지 컴파일 타임 검증
var _ news.NewsFetcher = (*mockFetcher)(nil)
