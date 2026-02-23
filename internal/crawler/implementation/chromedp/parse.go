package chromedp

import (
  "context"
  "fmt"
  "strings"
  "time"

  "github.com/PuerkitoBio/goquery"
  "github.com/chromedp/chromedp"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/pkg/logger"
)

// FetchAndParse: 페이지를 렌더링하고 바로 파싱
// 브라우저로 렌더링 → goquery로 파싱하는 2단계 처리
func (c *ChromedpCrawler) FetchAndParse(ctx context.Context, target core.Target, selectors map[string]string) (*core.Content, error) {
  log := logger.FromContext(ctx)

  // 렌더링된 HTML 가져오기
  raw, err := c.Fetch(ctx, target)
  if err != nil {
    return nil, err
  }

  // goquery로 파싱
  doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
  if err != nil {
    return nil, &core.CrawlerError{
      Category: core.ErrCategoryParse,
      Code:     "CDP_003",
      Message:  "failed to parse rendered HTML",
      Source:   c.name,
      URL:      target.URL,
      Err:      err,
    }
  }

  content := &core.Content{
    ID:           raw.ID,
    SourceID:     c.sourceInfo.Name,
    Country:      c.sourceInfo.Country,
    Language:     c.sourceInfo.Language,
    URL:          target.URL,
    CanonicalURL: target.URL,
    SourceType:   c.sourceInfo.Type,
    Reliability:  0.0,
    Extra:        make(map[string]interface{}),
    CreatedAt:    time.Now(),
  }

  // Extract title
  if titleSelector, ok := selectors["title"]; ok {
    content.Title = strings.TrimSpace(doc.Find(titleSelector).First().Text())
  }

  // Extract body
  if bodySelector, ok := selectors["body"]; ok {
    var bodyParts []string
    doc.Find(bodySelector).Each(func(i int, s *goquery.Selection) {
      bodyParts = append(bodyParts, s.Text())
    })
    content.Body = strings.TrimSpace(strings.Join(bodyParts, "\n"))
  }

  // Extract author
  if authorSelector, ok := selectors["author"]; ok {
    content.Author = strings.TrimSpace(doc.Find(authorSelector).First().Text())
  }

  // Extract images
  if imgSelector, ok := selectors["images"]; ok {
    doc.Find(imgSelector).Each(func(i int, s *goquery.Selection) {
      if src, exists := s.Attr("src"); exists {
        content.ImageURLs = append(content.ImageURLs, src)
      }
    })
  }

  content.WordCount = len(strings.Fields(content.Body))

  log.WithFields(map[string]interface{}{
    "title_length": len(content.Title),
    "body_length":  len(content.Body),
    "word_count":   content.WordCount,
    "image_count":  len(content.ImageURLs),
  }).Info("content parsed successfully with chromedp")

  if content.Title == "" || content.Body == "" {
    return nil, &core.CrawlerError{
      Category: core.ErrCategoryParse,
      Code:     "CDP_004",
      Message:  "missing required fields (title or body)",
      Source:   c.name,
      URL:      target.URL,
    }
  }

  return content, nil
}

// EvaluateJS: 페이지에서 JavaScript를 실행하고 결과 반환
// 복잡한 동적 컨텐츠 추출에 사용
func (c *ChromedpCrawler) EvaluateJS(ctx context.Context, url string, script string) (string, error) {
  if c.allocCtx == nil {
    return "", &core.CrawlerError{
      Category: core.ErrCategoryInternal,
      Code:     "CDP_001",
      Message:  "browser not initialized",
      Source:   c.name,
      URL:      url,
    }
  }

  tabCtx, cancel := chromedp.NewContext(c.allocCtx)
  defer cancel()

  tabCtx, timeoutCancel := context.WithTimeout(tabCtx, c.config.Timeout)
  defer timeoutCancel()

  actions := c.buildFetchActions(url)

  var result string
  actions = append(actions,
    chromedp.Evaluate(script, &result),
  )

  if err := chromedp.Run(tabCtx, actions...); err != nil {
    return "", &core.CrawlerError{
      Category:  core.ErrCategoryInternal,
      Code:      "CDP_005",
      Message:   fmt.Sprintf("JS evaluation failed: %s", script),
      Source:    c.name,
      URL:       url,
      Retryable: false,
      Err:       err,
    }
  }

  return result, nil
}
