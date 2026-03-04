package naver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news/kr/naver"
)

func TestNaverParser_ParseArticle_성공(t *testing.T) {
	cfg := naver.DefaultNaverConfig()
	parser := naver.NewNaverParser(cfg)

	html := `<html><body>
    <div id="title_area"><span>테스트 기사 제목입니다</span></div>
    <div id="dic_area">
      <p>첫 번째 문단입니다.</p>
      <p>두 번째 문단입니다.</p>
    </div>
    <em class="media_end_head_journalist_name">홍길동 기자</em>
    <span class="media_end_head_info_datestamp_time" data-date-time="2024-01-15T14:30:00+09:00"></span>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://n.news.naver.com/article/001/0014999999",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, "테스트 기사 제목입니다", article.Title)
	assert.Contains(t, article.Body, "첫 번째 문단입니다")
	assert.Contains(t, article.Body, "두 번째 문단입니다")
	assert.Equal(t, "홍길동 기자", article.Author)
	assert.Equal(t, raw.URL, article.URL)
	assert.False(t, article.PublishedAt.IsZero())
}

func TestNaverParser_ParseArticle_제목없음_오류반환(t *testing.T) {
	cfg := naver.DefaultNaverConfig()
	parser := naver.NewNaverParser(cfg)

	html := `<html><body>
    <div id="dic_area"><p>본문만 있습니다.</p></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://n.news.naver.com/article/001/0000000001",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.Nil(t, article)
	assert.Error(t, err)

	var crawlerErr *core.CrawlerError
	assert.ErrorAs(t, err, &crawlerErr)
	assert.Equal(t, "PARSE_002", crawlerErr.Code)
}

func TestNaverParser_ParseArticle_본문없음_오류반환(t *testing.T) {
	cfg := naver.DefaultNaverConfig()
	parser := naver.NewNaverParser(cfg)

	html := `<html><body>
    <div id="title_area"><span>제목만 있습니다</span></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://n.news.naver.com/article/001/0000000002",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.Nil(t, article)
	assert.Error(t, err)

	var crawlerErr *core.CrawlerError
	assert.ErrorAs(t, err, &crawlerErr)
	assert.Equal(t, "PARSE_002", crawlerErr.Code)
}

func TestNaverParser_ParseArticle_잘못된HTML_오류반환(t *testing.T) {
	cfg := naver.DefaultNaverConfig()
	parser := naver.NewNaverParser(cfg)

	// goquery는 broken HTML도 파싱하므로 nil HTML Reader 대신 빈 HTML로 테스트
	raw := &core.RawContent{
		URL:  "https://n.news.naver.com/article/001/0000000003",
		HTML: "",
	}

	article, err := parser.ParseArticle(raw)

	assert.Nil(t, article)
	assert.Error(t, err)
}

func TestNaverParser_ParseList_성공(t *testing.T) {
	cfg := naver.DefaultNaverConfig()
	parser := naver.NewNaverParser(cfg)

	html := `<html><body>
    <div class="sa_item">
      <a class="sa_text_title" href="https://news.naver.com/article/1">첫 번째 기사</a>
      <p class="sa_text_lede">첫 번째 기사 요약입니다.</p>
    </div>
    <div class="sa_item">
      <a class="sa_text_title" href="https://news.naver.com/article/2">두 번째 기사</a>
      <p class="sa_text_lede">두 번째 기사 요약입니다.</p>
    </div>
    <div class="sa_item">
      <!-- href 없는 항목은 skip -->
      <span class="sa_text_title">링크없는항목</span>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://news.naver.com/section/100",
		HTML: html,
	}

	items, err := parser.ParseList(raw)

	assert.NoError(t, err)
	assert.Len(t, items, 2)
	assert.Equal(t, "https://news.naver.com/article/1", items[0].URL)
	assert.Equal(t, "첫 번째 기사", items[0].Title)
	assert.Equal(t, "첫 번째 기사 요약입니다.", items[0].Summary)
	assert.Equal(t, "https://news.naver.com/article/2", items[1].URL)
}

func TestNaverParser_ParseList_항목없음_빈슬라이스반환(t *testing.T) {
	cfg := naver.DefaultNaverConfig()
	parser := naver.NewNaverParser(cfg)

	html := `<html><body><div class="no_news">뉴스가 없습니다.</div></body></html>`

	raw := &core.RawContent{
		URL:  "https://news.naver.com/section/100",
		HTML: html,
	}

	items, err := parser.ParseList(raw)

	assert.NoError(t, err)
	assert.Empty(t, items)
}
