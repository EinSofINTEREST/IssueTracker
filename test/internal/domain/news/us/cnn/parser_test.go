package cnn_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news/us/cnn"
)

func TestCNNParser_ParseArticle_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <h1 class="headline__text">CNN Test Article Title</h1>
    <div class="article__content">
      <p class="paragraph inline-placeholder">First paragraph content.</p>
      <p class="paragraph inline-placeholder">Second paragraph content.</p>
    </div>
    <div class="byline__names">
      <span class="byline__name">Jane Doe</span>
    </div>
    <div class="timestamp">Updated 2:30 PM EST, Mon March 3, 2026</div>
    <ol class="breadcrumb">
      <li><a href="/politics">Politics</a></li>
    </ol>
    <div class="metadata__tagline">
      <a>Breaking News</a>
      <a>US Politics</a>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/politics/test-article/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, "CNN Test Article Title", article.Title)
	assert.Contains(t, article.Body, "First paragraph content")
	assert.Contains(t, article.Body, "Second paragraph content")
	assert.Equal(t, "Jane Doe", article.Author)
	assert.Equal(t, "Politics", article.Category)
	assert.Equal(t, []string{"Breaking News", "US Politics"}, article.Tags)
	assert.Equal(t, raw.URL, article.URL)
	assert.False(t, article.PublishedAt.IsZero())
}

func TestCNNParser_ParseArticle_복수저자_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <h1 class="headline__text">Multi-Author Article</h1>
    <div class="article__content"><p>Article body here.</p></div>
    <div class="byline__names">
      <span class="byline__name">John Smith</span>
      <span class="byline__name">Jane Doe</span>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/politics/multi-author/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, "John Smith, Jane Doe", article.Author)
}

func TestCNNParser_ParseArticle_By접두사제거_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <h1 class="headline__text">Article with By prefix</h1>
    <div class="article__content"><p>Content here.</p></div>
    <div class="byline__names">
      <span class="byline__name">By John Smith, CNN</span>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/by-prefix/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, "John Smith, CNN", article.Author)
}

func TestCNNParser_ParseArticle_제목없음_오류반환(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <div class="article__content"><p>Body content only.</p></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/no-title/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.Nil(t, article)
	assert.Error(t, err)

	var crawlerErr *core.CrawlerError
	assert.ErrorAs(t, err, &crawlerErr)
	assert.Equal(t, "PARSE_002", crawlerErr.Code)
}

func TestCNNParser_ParseArticle_본문없음_오류반환(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <h1 class="headline__text">Title Only</h1>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/no-body/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.Nil(t, article)
	assert.Error(t, err)

	var crawlerErr *core.CrawlerError
	assert.ErrorAs(t, err, &crawlerErr)
	assert.Equal(t, "PARSE_002", crawlerErr.Code)
}

func TestCNNParser_ParseArticle_빈HTML_오류반환(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/empty/index.html",
		HTML: "",
	}

	article, err := parser.ParseArticle(raw)

	assert.Nil(t, article)
	assert.Error(t, err)
}

func TestCNNParser_ParseArticle_h1폴백_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	// h1.headline__text 대신 일반 h1 사용
	html := `<html><body>
    <h1>Fallback H1 Title</h1>
    <div class="article__content"><p>Article body content.</p></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/h1-fallback/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, "Fallback H1 Title", article.Title)
}

func TestCNNParser_ParseArticle_날짜_Updated접두사_UTC변환_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	// "Updated 2:30 PM EST, Mon March 3, 2026" EST(UTC-5) → UTC 19:30
	html := `<html><body>
    <h1 class="headline__text">Date Test Article</h1>
    <div class="article__content"><p>Body content.</p></div>
    <div class="timestamp">Updated 2:30 PM EST, Mon March 3, 2026</div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/date-test/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, time.UTC, article.PublishedAt.Location())
	assert.Equal(t, 2026, article.PublishedAt.Year())
	assert.Equal(t, time.March, article.PublishedAt.Month())
	assert.Equal(t, 3, article.PublishedAt.Day())
	assert.Equal(t, 19, article.PublishedAt.Hour()) // EST 14:30 → UTC 19:30
	assert.Equal(t, 30, article.PublishedAt.Minute())
}

func TestCNNParser_ParseArticle_날짜_접두사없음_UTC변환_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	// "8:00 AM EST, Mon March 3, 2026" EST → UTC 13:00
	html := `<html><body>
    <h1 class="headline__text">No Prefix Date Test</h1>
    <div class="article__content"><p>Body content.</p></div>
    <div class="timestamp">8:00 AM EST, Mon March 3, 2026</div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/no-prefix-date/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, time.UTC, article.PublishedAt.Location())
	assert.Equal(t, 13, article.PublishedAt.Hour()) // EST 08:00 → UTC 13:00
	assert.Equal(t, 0, article.PublishedAt.Minute())
}

func TestCNNParser_ParseArticle_날짜없음_현재시간반환(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <h1 class="headline__text">No Date Article</h1>
    <div class="article__content"><p>Body content.</p></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/no-date/index.html",
		HTML: html,
	}

	before := time.Now().UTC()
	article, err := parser.ParseArticle(raw)
	after := time.Now().UTC()

	assert.NoError(t, err)
	assert.True(t, article.PublishedAt.After(before) || article.PublishedAt.Equal(before))
	assert.True(t, article.PublishedAt.Before(after) || article.PublishedAt.Equal(after))
}

func TestCNNParser_ParseArticle_이미지URL_추출_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <h1 class="headline__text">Image Test Article</h1>
    <div class="article__content">
      <p>Body content.</p>
      <img src="https://media.cnn.com/api/v1/images/photo1.jpg" />
      <img data-src="https://media.cnn.com/api/v1/images/photo2.jpg" />
      <img src="data:image/gif;base64,R0lGOD" />
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.cnn.com/2026/03/03/us/images/index.html",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	// data: URL은 제외, data-src 우선
	assert.Len(t, article.ImageURLs, 2)
	assert.Contains(t, article.ImageURLs, "https://media.cnn.com/api/v1/images/photo1.jpg")
	assert.Contains(t, article.ImageURLs, "https://media.cnn.com/api/v1/images/photo2.jpg")
}

func TestCNNParser_ParseList_성공(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body>
    <div class="container__item">
      <a class="container__link" href="/2026/03/03/politics/article1/index.html">
        <span class="container__headline-text">First Article Title</span>
      </a>
      <div class="container__description">First article summary.</div>
    </div>
    <div class="container__item">
      <a class="container__link" href="https://edition.cnn.com/2026/03/03/world/article2/index.html">
        <span class="container__headline-text">Second Article Title</span>
      </a>
      <div class="container__description">Second article summary.</div>
    </div>
    <div class="container__item">
      <!-- href 없는 항목은 skip -->
      <span class="container__link">No Link Item</span>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://edition.cnn.com/politics",
		HTML: html,
	}

	items, err := parser.ParseList(raw)

	assert.NoError(t, err)
	assert.Len(t, items, 2)
	// 상대 경로는 절대 URL로 변환됨
	assert.Equal(t, "https://edition.cnn.com/2026/03/03/politics/article1/index.html", items[0].URL)
	assert.Equal(t, "First Article Title", items[0].Title)
	assert.Equal(t, "First article summary.", items[0].Summary)
	assert.Equal(t, "https://edition.cnn.com/2026/03/03/world/article2/index.html", items[1].URL)
}

func TestCNNParser_ParseList_항목없음_빈슬라이스반환(t *testing.T) {
	cfg := cnn.DefaultCNNConfig()
	parser := cnn.NewCNNParser(cfg)

	html := `<html><body><div class="no_news">No news available.</div></body></html>`

	raw := &core.RawContent{
		URL:  "https://edition.cnn.com/politics",
		HTML: html,
	}

	items, err := parser.ParseList(raw)

	assert.NoError(t, err)
	assert.Empty(t, items)
}
