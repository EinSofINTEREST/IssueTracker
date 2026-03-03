package daum_test

import (
  "testing"
  "time"

  "github.com/stretchr/testify/assert"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/domain/news/kr/daum"
)

func TestDaumParser_ParseArticle_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  html := `<html><body>
    <h3 class="tit_view">다음 뉴스 테스트 기사 제목</h3>
    <div class="article_view">
      <p>첫 번째 문단입니다.</p>
      <p>두 번째 문단입니다.</p>
    </div>
    <div class="info_view">
      <span class="txt_info">이승환 기자 이정후 기자 임윤지 기자</span>
      <span class="info_pub">
        <span class="num_date">2026.03.03. 14:30:00</span>
      </span>
      <a class="info_cate">사회</a>
    </div>
    <div class="keyword_area">
      <a>뉴스</a>
      <a>테스트</a>
    </div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/20260303143000001",
    HTML: html,
  }

  article, err := parser.ParseArticle(raw)

  assert.NoError(t, err)
  assert.Equal(t, "다음 뉴스 테스트 기사 제목", article.Title)
  assert.Contains(t, article.Body, "첫 번째 문단입니다")
  assert.Contains(t, article.Body, "두 번째 문단입니다")
  assert.Equal(t, "이승환 기자 이정후 기자 임윤지 기자", article.Author)
  assert.Equal(t, "사회", article.Category)
  assert.Equal(t, []string{"뉴스", "테스트"}, article.Tags)
  assert.Equal(t, raw.URL, article.URL)
  assert.False(t, article.PublishedAt.IsZero())
}

func TestDaumParser_ParseArticle_복수기자_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  // 다음 뉴스는 복수 기자를 단일 span.txt_info에 공백으로 이어 붙여 제공
  html := `<html><body>
    <h3 class="tit_view">복수 기자 기사 제목</h3>
    <div class="article_view"><p>기사 본문 내용입니다.</p></div>
    <div class="info_view">
      <span class="txt_info">홍길동 기자 이순신 기자</span>
    </div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/20260303000001",
    HTML: html,
  }

  article, err := parser.ParseArticle(raw)

  assert.NoError(t, err)
  assert.Equal(t, "홍길동 기자 이순신 기자", article.Author)
}

func TestDaumParser_ParseArticle_제목없음_오류반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  html := `<html><body>
    <div class="article_view"><p>본문만 있습니다.</p></div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/test001",
    HTML: html,
  }

  article, err := parser.ParseArticle(raw)

  assert.Nil(t, article)
  assert.Error(t, err)

  var crawlerErr *core.CrawlerError
  assert.ErrorAs(t, err, &crawlerErr)
  assert.Equal(t, "PARSE_002", crawlerErr.Code)
}

func TestDaumParser_ParseArticle_본문없음_오류반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  html := `<html><body>
    <h3 class="tit_view">제목만 있습니다</h3>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/test002",
    HTML: html,
  }

  article, err := parser.ParseArticle(raw)

  assert.Nil(t, article)
  assert.Error(t, err)

  var crawlerErr *core.CrawlerError
  assert.ErrorAs(t, err, &crawlerErr)
  assert.Equal(t, "PARSE_002", crawlerErr.Code)
}

func TestDaumParser_ParseArticle_빈HTML_오류반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/test003",
    HTML: "",
  }

  article, err := parser.ParseArticle(raw)

  assert.Nil(t, article)
  assert.Error(t, err)
}

func TestDaumParser_ParseArticle_날짜24시간제_UTC변환_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  // 첫 번째 span.txt_info: 기자명 / 두 번째 span.txt_info: 날짜
  // "2026.03.03. 14:30:00" KST → UTC 05:30:00
  html := `<html><body>
    <h3 class="tit_view">날짜 변환 테스트</h3>
    <div class="article_view"><p>본문 내용입니다.</p></div>
    <div class="info_view">
      <span class="txt_info">김기자</span>
      <span class="txt_info">2026.03.03. 14:30:00</span>
    </div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/20260303143000",
    HTML: html,
  }

  article, err := parser.ParseArticle(raw)

  assert.NoError(t, err)
  assert.Equal(t, time.UTC, article.PublishedAt.Location())
  assert.Equal(t, 2026, article.PublishedAt.Year())
  assert.Equal(t, time.March, article.PublishedAt.Month())
  assert.Equal(t, 3, article.PublishedAt.Day())
  assert.Equal(t, 5, article.PublishedAt.Hour())   // KST 14:30 → UTC 05:30
  assert.Equal(t, 30, article.PublishedAt.Minute())
}

func TestDaumParser_ParseArticle_날짜한국어오전_UTC변환_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  // "2026. 3. 3. 오전 9:30" KST → UTC 00:30
  html := `<html><body>
    <h3 class="tit_view">오전 날짜 변환 테스트</h3>
    <div class="article_view"><p>본문 내용입니다.</p></div>
    <div class="info_view">
      <span class="txt_info">김기자</span>
      <span class="txt_info">2026. 3. 3. 오전 9:30</span>
    </div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/20260303093000",
    HTML: html,
  }

  article, err := parser.ParseArticle(raw)

  assert.NoError(t, err)
  assert.Equal(t, time.UTC, article.PublishedAt.Location())
  assert.Equal(t, 0, article.PublishedAt.Hour())   // KST 09:30 → UTC 00:30
  assert.Equal(t, 30, article.PublishedAt.Minute())
}

func TestDaumParser_ParseArticle_날짜한국어오후_UTC변환_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  // "2026. 3. 3. 오후 2:30" KST → UTC 05:30
  html := `<html><body>
    <h3 class="tit_view">오후 날짜 변환 테스트</h3>
    <div class="article_view"><p>본문 내용입니다.</p></div>
    <div class="info_view">
      <span class="txt_info">김기자</span>
      <span class="txt_info">2026. 3. 3. 오후 2:30</span>
    </div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/20260303143000k",
    HTML: html,
  }

  article, err := parser.ParseArticle(raw)

  assert.NoError(t, err)
  assert.Equal(t, time.UTC, article.PublishedAt.Location())
  assert.Equal(t, 5, article.PublishedAt.Hour())   // KST 오후 2:30 → UTC 05:30
  assert.Equal(t, 30, article.PublishedAt.Minute())
}

func TestDaumParser_ParseArticle_날짜없음_현재시간반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  // span.txt_info가 하나뿐(기자명만)이면 두 번째 요소가 없으므로 현재시간 반환
  html := `<html><body>
    <h3 class="tit_view">날짜 없는 기사</h3>
    <div class="article_view"><p>본문 내용입니다.</p></div>
    <div class="info_view">
      <span class="txt_info">김기자</span>
    </div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/nodate",
    HTML: html,
  }

  before := time.Now().UTC()
  article, err := parser.ParseArticle(raw)
  after := time.Now().UTC()

  assert.NoError(t, err)
  assert.True(t, article.PublishedAt.After(before) || article.PublishedAt.Equal(before))
  assert.True(t, article.PublishedAt.Before(after) || article.PublishedAt.Equal(after))
}

func TestDaumParser_ParseArticle_이미지URL_추출_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  html := `<html><body>
    <h3 class="tit_view">이미지 포함 기사</h3>
    <div class="article_view">
      <p>본문 내용입니다.</p>
      <img src="https://img1.kakaocdn.net/thumb/photo1.jpg" />
      <img data-src="https://img1.kakaocdn.net/thumb/photo2.jpg" />
      <img src="data:image/gif;base64,R0lGOD" />
    </div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://v.daum.net/v/imgtest",
    HTML: html,
  }

  article, err := parser.ParseArticle(raw)

  assert.NoError(t, err)
  // data: URL은 제외, data-src 우선, src 폴백
  assert.Len(t, article.ImageURLs, 2)
  assert.Contains(t, article.ImageURLs, "https://img1.kakaocdn.net/thumb/photo1.jpg")
  assert.Contains(t, article.ImageURLs, "https://img1.kakaocdn.net/thumb/photo2.jpg")
}

func TestDaumParser_ParseList_성공(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  html := `<html><body>
    <div class="item_issue">
      <a class="link_txt" href="https://v.daum.net/v/article1">첫 번째 기사</a>
      <p class="desc_txt">첫 번째 기사 요약입니다.</p>
    </div>
    <div class="item_issue">
      <a class="link_txt" href="https://v.daum.net/v/article2">두 번째 기사</a>
      <p class="desc_txt">두 번째 기사 요약입니다.</p>
    </div>
    <div class="item_issue">
      <!-- href 없는 항목은 skip -->
      <span class="link_txt">링크없는항목</span>
    </div>
  </body></html>`

  raw := &core.RawContent{
    URL:  "https://news.daum.net/politics",
    HTML: html,
  }

  items, err := parser.ParseList(raw)

  assert.NoError(t, err)
  assert.Len(t, items, 2)
  assert.Equal(t, "https://v.daum.net/v/article1", items[0].URL)
  assert.Equal(t, "첫 번째 기사", items[0].Title)
  assert.Equal(t, "첫 번째 기사 요약입니다.", items[0].Summary)
  assert.Equal(t, "https://v.daum.net/v/article2", items[1].URL)
}

func TestDaumParser_ParseList_항목없음_빈슬라이스반환(t *testing.T) {
  cfg := daum.DefaultDaumConfig()
  parser := daum.NewDaumParser(cfg)

  html := `<html><body><div class="no_news">뉴스가 없습니다.</div></body></html>`

  raw := &core.RawContent{
    URL:  "https://news.daum.net/politics",
    HTML: html,
  }

  items, err := parser.ParseList(raw)

  assert.NoError(t, err)
  assert.Empty(t, items)
}
