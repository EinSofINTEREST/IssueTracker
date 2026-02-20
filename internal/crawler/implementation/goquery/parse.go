package goquery

import (
  "context"
  "fmt"
  "net/http"
  "strings"
  "time"

  "github.com/PuerkitoBio/goquery"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/pkg/logger"
)

// FetchAndParse: URL에서 컨텐츠를 가져와서 바로 파싱
// goquery의 장점을 활용하여 한 번에 처리
func (c *GoqueryCrawler) FetchAndParse(ctx context.Context, target core.Target, selectors map[string]string) (*core.Article, error) {
  log := logger.FromContext(ctx)

  // HTTP 요청
  req, err := http.NewRequestWithContext(ctx, "GET", target.URL, nil)
  if err != nil {
    return nil, err
  }

  req.Header.Set("User-Agent", c.config.UserAgent)

  resp, err := c.httpClient.Do(req)
  if err != nil {
    return nil, err
  }
  defer resp.Body.Close()

  // goquery Document 생성
  doc, err := goquery.NewDocumentFromReader(resp.Body)
  if err != nil {
    return nil, err
  }

  // Article 추출
  article := &core.Article{
    ID:           fmt.Sprintf("%s-%d", c.name, time.Now().UnixNano()),
    SourceID:     c.sourceInfo.Name,
    Country:      c.sourceInfo.Country,
    Language:     c.sourceInfo.Language,
    URL:          target.URL,
    CanonicalURL: target.URL,
    CreatedAt:    time.Now(),
  }

  // Extract title
  if titleSelector, ok := selectors["title"]; ok {
    article.Title = strings.TrimSpace(doc.Find(titleSelector).First().Text())
  }

  // Extract body
  if bodySelector, ok := selectors["body"]; ok {
    var bodyParts []string
    doc.Find(bodySelector).Each(func(i int, s *goquery.Selection) {
      bodyParts = append(bodyParts, s.Text())
    })
    article.Body = strings.TrimSpace(strings.Join(bodyParts, "\n"))
  }

  // Extract author
  if authorSelector, ok := selectors["author"]; ok {
    article.Author = strings.TrimSpace(doc.Find(authorSelector).First().Text())
  }

  // Extract images
  if imgSelector, ok := selectors["images"]; ok {
    doc.Find(imgSelector).Each(func(i int, s *goquery.Selection) {
      if src, exists := s.Attr("src"); exists {
        article.ImageURLs = append(article.ImageURLs, src)
      }
    })
  }

  article.WordCount = len(strings.Fields(article.Body))

  log.WithFields(map[string]interface{}{
    "title_length": len(article.Title),
    "body_length":  len(article.Body),
    "word_count":   article.WordCount,
    "image_count":  len(article.ImageURLs),
  }).Info("article parsed successfully with goquery")

  // Validation
  if article.Title == "" || article.Body == "" {
    return nil, &core.CrawlerError{
      Category: core.ErrCategoryParse,
      Code:     "PARSE_003",
      Message:  "missing required fields",
      Source:   c.name,
      URL:      target.URL,
    }
  }

  return article, nil
}
