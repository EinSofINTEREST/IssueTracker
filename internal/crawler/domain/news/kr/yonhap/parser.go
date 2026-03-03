package yonhap

import (
  "strings"
  "time"

  "github.com/PuerkitoBio/goquery"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/domain/news"
)

// YonhapParser는 연합뉴스 기사와 목록 페이지를 파싱합니다.
// news.NewsArticleParser와 news.NewsListParser를 구현합니다.
//
// YonhapParser implements NewsArticleParser and NewsListParser for Yonhap News.
type YonhapParser struct {
  config YonhapConfig
}

// NewYonhapParser는 새로운 YonhapParser를 생성합니다.
func NewYonhapParser(config YonhapConfig) *YonhapParser {
  return &YonhapParser{config: config}
}

// ParseArticle은 연합뉴스 기사 페이지를 파싱합니다.
// title 또는 body가 비어있으면 PARSE_002 에러를 반환합니다.
// Category는 파싱하지 않으며 classifier가 별도로 설정합니다.
func (p *YonhapParser) ParseArticle(raw *core.RawContent) (*news.NewsArticle, error) {
  doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
  if err != nil {
    return nil, core.NewParseError("PARSE_001", "failed to parse yonhap article html", raw.URL, err)
  }

  article := &news.NewsArticle{
    URL:         raw.URL,
    Title:       strings.TrimSpace(doc.Find(p.config.ArticleSelectors["title"]).First().Text()),
    Author:      p.extractAuthors(doc),
    Body:        p.extractBody(doc),
    Tags:        p.extractTags(doc),
    ImageURLs:   p.extractImageURLs(doc),
    PublishedAt: p.extractDate(doc),
    Category:    p.extractCategory(doc),
  }

  if article.Title == "" || article.Body == "" {
    return nil, core.NewParseError("PARSE_002", "missing required fields in yonhap article", raw.URL, nil)
  }

  return article, nil
}

// ParseList는 연합뉴스 목록 페이지에서 기사 링크를 추출합니다.
func (p *YonhapParser) ParseList(raw *core.RawContent) ([]news.NewsItem, error) {
  doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
  if err != nil {
    return nil, core.NewParseError("PARSE_001", "failed to parse yonhap list html", raw.URL, err)
  }

  var items []news.NewsItem

  doc.Find(p.config.ListSelectors["item"]).Each(func(_ int, s *goquery.Selection) {
    linkEl := s.Find(p.config.ListSelectors["link"])
    href, exists := linkEl.Attr("href")
    if !exists || href == "" {
      return
    }

    items = append(items, news.NewsItem{
      URL:     href,
      Title:   strings.TrimSpace(s.Find(p.config.ListSelectors["title"]).Text()),
      Summary: strings.TrimSpace(s.Find(p.config.ListSelectors["summary"]).Text()),
    })
  })

  return items, nil
}

// extractBody는 <div class="story-news article"> 하위 <p> 태그 텍스트를 추출합니다.
// 광고·스크립트·스타일 요소를 먼저 제거합니다.
func (p *YonhapParser) extractBody(doc *goquery.Document) string {
  bodyEl := doc.Find(p.config.ArticleSelectors["body"])
  bodyEl.Find("script, style, .ad-wrap, .banner").Remove()

  var parts []string
  bodyEl.Find("p").Each(func(_ int, s *goquery.Selection) {
    text := strings.TrimSpace(s.Text())
    if text != "" {
      parts = append(parts, text)
    }
  })

  // <p> 태그가 없으면 div 전체 텍스트 사용
  if len(parts) == 0 {
    text := strings.TrimSpace(bodyEl.Text())
    if text != "" {
      parts = append(parts, text)
    }
  }

  return strings.Join(parts, "\n")
}

// extractAuthors는 기자 정보 영역(#newsWriterCarousel01)에서 이름을 추출합니다.
// 복수 기자를 모두 수집하여 ", "로 구분한 문자열을 반환합니다.
// 경로: #newsWriterCarousel01 > div > div > div > div > strong
func (p *YonhapParser) extractAuthors(doc *goquery.Document) string {
  zone := doc.Find(p.config.ArticleSelectors["author"])

  var names []string
  zone.Find("div > div > div > div > strong").Each(func(_ int, s *goquery.Selection) {
    name := strings.TrimSpace(s.Text())
    if name != "" {
      names = append(names, name)
    }
  })

  return strings.Join(names, ", ")
}

// extractTags는 키워드 영역(.keyword-zone)에서 태그 목록을 추출합니다.
// 경로: .keyword-zone > div > ul > li > a
func (p *YonhapParser) extractTags(doc *goquery.Document) []string {
  zone := doc.Find(p.config.ArticleSelectors["tags"])

  var tags []string
  zone.Find("div > ul > li > a").Each(func(_ int, s *goquery.Selection) {
    tag := strings.TrimSpace(s.Text())
    if tag != "" {
      tags = append(tags, tag)
    }
  })

  return tags
}

// extractImageURLs는 사진 영역(.comp-box.photo-group)에서 이미지 URL을 추출합니다.
// 경로: .comp-box.photo-group > figure > div > span > img[src]
func (p *YonhapParser) extractImageURLs(doc *goquery.Document) []string {
  zone := doc.Find(p.config.ArticleSelectors["images"])

  var urls []string
  zone.Find("figure > div > span > img").Each(func(_ int, s *goquery.Selection) {
    src, exists := s.Attr("src")
    if exists && src != "" {
      urls = append(urls, src)
    }
  })

  return urls
}

// extractCategory는 <body> 태그의 data-pagecode 속성에서 카테고리를 추출합니다.
func (p *YonhapParser) extractCategory(doc *goquery.Document) string {
  pageCode, exists := doc.Find("body").Attr("data-pagecode")
  if !exists || pageCode == "" {
    return ""
  }
  return pageCode
}

// extractDate는 연합뉴스 날짜를 <div class="update-time"> 태그의
// data-published-time 속성에서 추출하여 time.Time으로 파싱합니다.
// 파싱 실패 시 현재 시간을 반환합니다.
func (p *YonhapParser) extractDate(doc *goquery.Document) time.Time {
  dateStr, exists := doc.Find(p.config.ArticleSelectors["date"]).Attr("data-published-time")
  if !exists || dateStr == "" {
    return time.Now()
  }

  // 연합뉴스 날짜 형식: "2024-01-15 14:30" (KST)
  loc, err := time.LoadLocation("Asia/Seoul")
  if err != nil {
    return time.Now().UTC()
  }

  t, err := time.ParseInLocation("2006-01-02 15:04", dateStr, loc)
  if err != nil {
    return time.Now().UTC()
  }

  // UTC로 변환하여 반환
  return t.UTC()
}
