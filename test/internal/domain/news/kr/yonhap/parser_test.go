package yonhap_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news/kr/yonhap"
)

func TestYonhapParser_ParseArticle_성공(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body data-pagecode="AKR">
    <h1 class="tit01">연합뉴스 기사 제목</h1>
    <div class="story-news article">
      <p>연합뉴스 기사 본문 첫 번째 문단입니다.</p>
      <p>연합뉴스 기사 본문 두 번째 문단입니다.</p>
    </div>
    <div id="newsWriterCarousel01" class="writer-zone01">
      <div><div><div><div><strong>박연합 기자</strong></div></div></div></div>
    </div>
    <div class="update-time" data-published-time="2024-01-15 14:30"></div>
    <div class="keyword-zone">
      <div><ul>
        <li><a>한반도</a></li>
        <li><a>외교</a></li>
      </ul></div>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/view/AKR20240115000000001",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, "연합뉴스 기사 제목", article.Title)
	assert.Contains(t, article.Body, "연합뉴스 기사 본문 첫 번째 문단입니다")
	assert.Contains(t, article.Body, "연합뉴스 기사 본문 두 번째 문단입니다")
	assert.Equal(t, "박연합 기자", article.Author)
	assert.Equal(t, raw.URL, article.URL)
	assert.False(t, article.PublishedAt.IsZero())
	assert.Equal(t, []string{"한반도", "외교"}, article.Tags)
	assert.Equal(t, "AKR", article.Category)
}

func TestYonhapParser_ParseArticle_이미지URL_추출(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body data-pagecode="AKR">
    <h1 class="tit01">이미지 포함 기사</h1>
    <div class="story-news article"><p>본문입니다.</p></div>
    <div class="update-time" data-published-time="2024-01-15 14:30"></div>
    <div class="comp-box photo-group">
      <figure><div><span><img src="https://img.yna.co.kr/photo/001.jpg"/></span></div></figure>
      <figure><div><span><img src="https://img.yna.co.kr/photo/002.jpg"/></span></div></figure>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/view/AKR20240115000000004",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, []string{
		"https://img.yna.co.kr/photo/001.jpg",
		"https://img.yna.co.kr/photo/002.jpg",
	}, article.ImageURLs)
}

func TestYonhapParser_ParseArticle_복수기자_성공(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body data-pagecode="POL">
    <h1 class="tit01">공동 취재 기사</h1>
    <div class="story-news article"><p>본문입니다.</p></div>
    <div class="update-time" data-published-time="2024-01-15 14:30"></div>
    <div id="newsWriterCarousel01" class="writer-zone01">
      <div><div><div><div><strong>김기자</strong></div></div></div></div>
      <div><div><div><div><strong>이기자</strong></div></div></div></div>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/view/AKR20240115000000003",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Equal(t, "김기자, 이기자", article.Author)
	assert.Equal(t, "POL", article.Category)
}

func TestYonhapParser_ParseArticle_카테고리없음_빈문자열반환(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body>
    <h1 class="tit01">카테고리 없는 기사</h1>
    <div class="story-news article"><p>본문입니다.</p></div>
    <div class="update-time" data-published-time="2024-01-15 14:30"></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/view/AKR20240115000000005",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.Empty(t, article.Category)
}

func TestYonhapParser_ParseArticle_제목없음_오류반환(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body>
    <div class="story-news article"><p>본문만 있습니다.</p></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/view/AKR00000000000000001",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.Nil(t, article)
	assert.Error(t, err)

	var crawlerErr *core.CrawlerError
	assert.ErrorAs(t, err, &crawlerErr)
	assert.Equal(t, "PARSE_002", crawlerErr.Code)
}

func TestYonhapParser_ParseArticle_본문없음_오류반환(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body>
    <h1 class="tit01">제목만 있음</h1>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/view/AKR00000000000000002",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.Nil(t, article)
	assert.Error(t, err)

	var crawlerErr *core.CrawlerError
	assert.ErrorAs(t, err, &crawlerErr)
	assert.Equal(t, "PARSE_002", crawlerErr.Code)
}

func TestYonhapParser_ParseArticle_날짜속성없음_현재시간반환(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body data-pagecode="AKR">
    <h1 class="tit01">날짜 없는 기사</h1>
    <div class="story-news article"><p>본문입니다.</p></div>
    <div class="update-time"></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/view/AKR20240115000000006",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	assert.False(t, article.PublishedAt.IsZero())
}

func TestYonhapParser_ParseArticle_날짜UTC변환_성공(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	// KST 14:30 = UTC 05:30 (KST는 UTC+9)
	html := `<html><body data-pagecode="AKR">
    <h1 class="tit01">UTC 변환 테스트</h1>
    <div class="story-news article"><p>본문입니다.</p></div>
    <div class="update-time" data-published-time="2024-01-15 14:30"></div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/view/AKR20240115000000007",
		HTML: html,
	}

	article, err := parser.ParseArticle(raw)

	assert.NoError(t, err)
	
	// 타임존이 UTC인지 확인
	assert.Equal(t, "UTC", article.PublishedAt.Location().String())
	
	// KST 2024-01-15 14:30 = UTC 2024-01-15 05:30
	expectedUTC := time.Date(2024, 1, 15, 5, 30, 0, 0, time.UTC)
	assert.Equal(t, expectedUTC, article.PublishedAt)
}

func TestYonhapParser_ParseList_성공(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body>
    <div class="alist-item">
      <a href="https://www.yna.co.kr/view/AKR20240115000000001">
        <span class="alist-item-txt">첫 번째 기사</span>
      </a>
      <p class="lead">첫 번째 기사 요약</p>
    </div>
    <div class="alist-item">
      <a href="https://www.yna.co.kr/view/AKR20240115000000002">
        <span class="alist-item-txt">두 번째 기사</span>
      </a>
    </div>
    <div class="alist-item">
      <!-- href 없는 항목 skip -->
      <span>링크없음</span>
    </div>
  </body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/politics/all",
		HTML: html,
	}

	items, err := parser.ParseList(raw)

	assert.NoError(t, err)
	assert.Len(t, items, 2)
	assert.Equal(t, "https://www.yna.co.kr/view/AKR20240115000000001", items[0].URL)
	assert.Equal(t, "첫 번째 기사 요약", items[0].Summary)
	assert.Equal(t, "https://www.yna.co.kr/view/AKR20240115000000002", items[1].URL)
}

func TestYonhapParser_ParseList_항목없음_빈슬라이스반환(t *testing.T) {
	cfg := yonhap.DefaultYonhapConfig()
	parser := yonhap.NewYonhapParser(cfg)

	html := `<html><body><div class="no_news">기사 없음</div></body></html>`

	raw := &core.RawContent{
		URL:  "https://www.yna.co.kr/politics/all",
		HTML: html,
	}

	items, err := parser.ParseList(raw)

	assert.NoError(t, err)
	assert.Empty(t, items)
}
