package cnn_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news/us/cnn"
)

// mockFetcher는 테스트용 NewsFetcher 구현입니다.
type mockFetcher struct {
	raw *core.RawContent
	err error
}

func (m *mockFetcher) Fetch(_ context.Context, _ core.Target) (*core.RawContent, error) {
	return m.raw, m.err
}

func TestCNNCrawler_Name(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)
	crawler := cnn.NewCNNCrawler(cfg, &mockFetcher{}, parser, nil)

	assert.Equal(t, "cnn", crawler.Name())
}

func TestCNNCrawler_Source(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)
	crawler := cnn.NewCNNCrawler(cfg, &mockFetcher{}, parser, nil)

	src := crawler.Source()
	assert.Equal(t, "US", src.Country)
	assert.Equal(t, "en", src.Language)
	assert.Equal(t, "cnn", src.Name)
}

func TestCNNCrawler_Initialize(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)
	crawler := cnn.NewCNNCrawler(cfg, &mockFetcher{}, parser, nil)

	newCfg := core.DefaultConfig()
	newCfg.RequestsPerHour = 50

	err := crawler.Initialize(context.Background(), newCfg)

	assert.NoError(t, err)
}

func TestCNNCrawler_FetchArticle_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <h1 class="headline__text">Test CNN Article</h1>
    <div class="article__content"><p>Article body content.</p></div>
    <span class="byline__name">John Doe</span>
    <div class="timestamp">Updated 10:00 AM EST, Mon March 3, 2026</div>
  </body></html>`

	fetcher := &mockFetcher{
		raw: &core.RawContent{
			URL:  "https://www.cnn.com/2026/03/03/us/test-article/index.html",
			HTML: html,
		},
	}

	crawler := cnn.NewCNNCrawler(cfg, fetcher, parser, nil)

	article, err := crawler.FetchArticle(context.Background(), "https://www.cnn.com/2026/03/03/us/test-article/index.html")

	assert.NoError(t, err)
	assert.NotNil(t, article)
	assert.Equal(t, "Test CNN Article", article.Title)
	assert.Contains(t, article.Body, "Article body content")
	assert.Equal(t, "John Doe", article.Author)
}

func TestCNNCrawler_FetchArticle_fetch실패(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	fetcher := &mockFetcher{
		err: errors.New("network error"),
	}

	crawler := cnn.NewCNNCrawler(cfg, fetcher, parser, nil)

	article, err := crawler.FetchArticle(context.Background(), "https://www.cnn.com/2026/03/03/us/test/index.html")

	assert.Nil(t, article)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cnn fetch article")
}

func TestCNNCrawler_FetchArticle_파싱실패(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	fetcher := &mockFetcher{
		raw: &core.RawContent{
			URL:  "https://www.cnn.com/2026/03/03/us/no-content/index.html",
			HTML: "<html><body><p>No title or body selectors match.</p></body></html>",
		},
	}

	crawler := cnn.NewCNNCrawler(cfg, fetcher, parser, nil)

	article, err := crawler.FetchArticle(context.Background(), "https://www.cnn.com/2026/03/03/us/no-content/index.html")

	assert.Nil(t, article)
	assert.Error(t, err)
}

func TestCNNCrawler_FetchList_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <div class="container__item">
      <a class="container__link" href="/2026/03/03/politics/article1/index.html">
        <span class="container__headline-text">Article One</span>
      </a>
    </div>
    <div class="container__item">
      <a class="container__link" href="/2026/03/03/world/article2/index.html">
        <span class="container__headline-text">Article Two</span>
      </a>
    </div>
  </body></html>`

	fetcher := &mockFetcher{
		raw: &core.RawContent{
			URL:  "https://edition.cnn.com/politics",
			HTML: html,
		},
	}

	crawler := cnn.NewCNNCrawler(cfg, fetcher, parser, nil)

	target := core.Target{
		URL:  "https://edition.cnn.com/politics",
		Type: core.TargetTypeCategory,
	}

	items, err := crawler.FetchList(context.Background(), target)

	assert.NoError(t, err)
	assert.Len(t, items, 2)
	assert.Equal(t, "Article One", items[0].Title)
	assert.Equal(t, "Article Two", items[1].Title)
}

func TestCNNCrawler_FetchList_fetch실패(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	fetcher := &mockFetcher{
		err: errors.New("connection timeout"),
	}

	crawler := cnn.NewCNNCrawler(cfg, fetcher, parser, nil)

	target := core.Target{
		URL:  "https://edition.cnn.com/politics",
		Type: core.TargetTypeCategory,
	}

	items, err := crawler.FetchList(context.Background(), target)

	assert.Nil(t, items)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cnn fetch list")
}
