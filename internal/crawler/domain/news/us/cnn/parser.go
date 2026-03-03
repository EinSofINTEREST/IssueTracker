package cnn

import (
  "strings"
  "time"

  "github.com/PuerkitoBio/goquery"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/domain/news"
)

// CNNParser는 CNN 기사와 목록 페이지를 파싱합니다.
// news.NewsArticleParser와 news.NewsListParser를 구현합니다.
//
// CNNParser implements NewsArticleParser and NewsListParser for CNN.
type CNNParser struct {
  config CNNConfig
}

// NewCNNParser는 새로운 CNNParser를 생성합니다.
func NewCNNParser(config CNNConfig) *CNNParser {
  return &CNNParser{config: config}
}

// ParseArticle은 CNN 기사 페이지를 파싱합니다.
// title 또는 body가 비어있으면 PARSE_002 에러를 반환합니다.
func (p *CNNParser) ParseArticle(raw *core.RawContent) (*news.NewsArticle, error) {
  doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
  if err != nil {
    return nil, core.NewParseError("PARSE_001", "failed to parse cnn article html", raw.URL, err)
  }

  imageURLs := p.extractImageURLs(doc)

  article := &news.NewsArticle{
    URL:         raw.URL,
    Title:       p.extractTitle(doc),
    Author:      p.extractAuthor(doc),
    Body:        p.extractBody(doc),
    Tags:        p.extractTags(doc),
    ImageURLs:   imageURLs,
    PublishedAt: p.extractDate(doc),
    Category:    p.extractCategory(doc),
  }

  if article.Title == "" || article.Body == "" {
    return nil, core.NewParseError("PARSE_002", "missing required fields in cnn article", raw.URL, nil)
  }

  return article, nil
}

// ParseList는 CNN 목록 페이지에서 기사 링크를 추출합니다.
func (p *CNNParser) ParseList(raw *core.RawContent) ([]news.NewsItem, error) {
  doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
  if err != nil {
    return nil, core.NewParseError("PARSE_001", "failed to parse cnn list html", raw.URL, err)
  }

  var items []news.NewsItem

  doc.Find(p.config.ListSelectors["item"]).Each(func(_ int, s *goquery.Selection) {
    linkEl := s.Find(p.config.ListSelectors["link"])
    href, exists := linkEl.Attr("href")
    if !exists || href == "" {
      return
    }

    // 상대 경로 → 절대 경로
    if strings.HasPrefix(href, "/") {
      href = "https://edition.cnn.com" + href
    }

    items = append(items, news.NewsItem{
      URL:     href,
      Title:   strings.TrimSpace(s.Find(p.config.ListSelectors["title"]).Text()),
      Summary: strings.TrimSpace(s.Find(p.config.ListSelectors["summary"]).Text()),
    })
  })

  return items, nil
}

// extractTitle은 기사 제목을 추출합니다.
// h1.headline__text를 먼저 시도하고 없으면 h1로 폴백합니다.
func (p *CNNParser) extractTitle(doc *goquery.Document) string {
  title := strings.TrimSpace(doc.Find(p.config.ArticleSelectors["title"]).First().Text())
  if title == "" {
    title = strings.TrimSpace(doc.Find("h1").First().Text())
  }
  return title
}

// extractAuthor는 저자를 추출합니다.
// 복수 저자는 쉼표로 구분하고, "By " 접두사는 제거합니다.
func (p *CNNParser) extractAuthor(doc *goquery.Document) string {
  var names []string
  doc.Find(p.config.ArticleSelectors["author"]).Each(func(_ int, s *goquery.Selection) {
    name := strings.TrimSpace(s.Text())
    name = strings.TrimPrefix(name, "By ")
    name = strings.TrimPrefix(name, "by ")
    if name != "" {
      names = append(names, name)
    }
  })
  return strings.Join(names, ", ")
}

// extractBody는 기사 본문 텍스트를 추출합니다.
// 광고, 관련 기사, 미디어 영역을 제거합니다.
func (p *CNNParser) extractBody(doc *goquery.Document) string {
  bodyDiv := doc.Find(p.config.ArticleSelectors["body"])
  bodyDiv.Find(
    "figure, .image__container, .video__container, .el__article--embed," +
      " .el__storyelement--standard-ad, .ad-slot, .related-content," +
      " .zn-body__read-more, script, style",
  ).Remove()

  var parts []string
  bodyDiv.Find("p").Each(func(_ int, s *goquery.Selection) {
    text := strings.TrimSpace(s.Text())
    if text != "" {
      parts = append(parts, text)
    }
  })

  // <p> 태그가 없으면 div 전체 텍스트 사용
  if len(parts) == 0 {
    text := strings.TrimSpace(bodyDiv.Text())
    if text != "" {
      parts = append(parts, text)
    }
  }

  return strings.Join(parts, "\n")
}

// extractTags는 태그 목록을 추출합니다.
func (p *CNNParser) extractTags(doc *goquery.Document) []string {
  sel := p.config.ArticleSelectors["tags"]
  if sel == "" {
    return nil
  }

  var tags []string
  doc.Find(sel).Each(func(_ int, s *goquery.Selection) {
    tag := strings.TrimSpace(s.Text())
    if tag != "" {
      tags = append(tags, tag)
    }
  })
  return tags
}

// extractImageURLs는 본문 영역의 이미지 URL을 추출합니다.
// data-src 우선(lazy loading), 없으면 src 폴백. data: URL은 제외합니다.
func (p *CNNParser) extractImageURLs(doc *goquery.Document) []string {
  var urls []string
  doc.Find(p.config.ArticleSelectors["image_urls"]).Each(func(_ int, s *goquery.Selection) {
    src := s.AttrOr("data-src", "")
    if src == "" {
      src = s.AttrOr("src", "")
    }
    if src != "" && !strings.HasPrefix(src, "data:") {
      urls = append(urls, src)
    }
  })
  return urls
}

// extractCategory는 브레드크럼에서 카테고리를 추출합니다.
func (p *CNNParser) extractCategory(doc *goquery.Document) string {
  category := doc.Find(p.config.ArticleSelectors["category"]).First().Text()
  return strings.TrimSpace(category)
}

// extractDate는 CNN 기사의 날짜를 파싱합니다.
// "Updated 3:30 PM EST, Mon March 3, 2026" 형식을 처리합니다.
// 파싱 실패 시 현재 시간을 반환합니다.
func (p *CNNParser) extractDate(doc *goquery.Document) time.Time {
  raw := strings.TrimSpace(doc.Find(p.config.ArticleSelectors["date"]).First().Text())
  if raw == "" {
    return time.Now().UTC()
  }
  return parseCNNDate(raw)
}

// cnnTZOffsets는 CNN이 사용하는 미국 시간대 약어를 RFC 3339 숫자 offset으로 매핑합니다.
// Go의 time.Parse는 시간대 약어를 UTC+0으로 처리하는 경우가 있으므로
// 숫자 offset("-0500" 형식)으로 치환 후 파싱합니다.
var cnnTZOffsets = []struct {
  abbr   string
  offset string
}{
  {"EDT", "-0400"},
  {"EST", "-0500"},
  {"CDT", "-0400"},
  {"CST", "-0600"},
  {"MDT", "-0600"},
  {"MST", "-0700"},
  {"PDT", "-0700"},
  {"PST", "-0800"},
  // "ET"는 마지막에 처리 (EST/EDT가 먼저 치환되어야 충돌 없음)
  {"ET", "-0500"},
}

// parseCNNDate는 CNN 날짜 문자열을 UTC time.Time으로 변환합니다.
// 지원 형식:
//   - "Updated 3:30 PM EST, Mon March 3, 2026"
//   - "3:30 PM EST, Mon March 3, 2026"
//   - "3:30 PM EST, March 3, 2026"
//   - "March 3, 2026"
func parseCNNDate(s string) time.Time {
  // "Updated" / "Published" 접두사 제거
  for _, prefix := range []string{"Updated ", "Published ", "UPDATED ", "PUBLISHED "} {
    s = strings.TrimPrefix(s, prefix)
  }
  s = strings.TrimSpace(s)

  // 시간대 약어를 숫자 offset으로 치환 (Go time.Parse 호환성)
  for _, tz := range cnnTZOffsets {
    s = strings.ReplaceAll(s, " "+tz.abbr+",", " "+tz.offset+",")
    s = strings.ReplaceAll(s, " "+tz.abbr+" ", " "+tz.offset+" ")
  }

  formats := []string{
    "3:04 PM -0700, Mon January 2, 2006",
    "3:04 PM -0700, Mon Jan 2, 2006",
    "3:04 PM -0700, January 2, 2006",
    "3:04 PM -0700, Jan 2, 2006",
    "January 2, 2006",
    "Jan 2, 2006",
  }

  for _, fmt := range formats {
    t, err := time.Parse(fmt, s)
    if err == nil {
      return t.UTC()
    }
  }

  return time.Now().UTC()
}
