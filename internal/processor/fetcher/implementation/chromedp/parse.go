package chromedp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/chromedp"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
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

	// Fetch 단계의 partial_load 마킹을 Content 로 전파 (이슈 #146 + CodeRabbit 피드백).
	// timeout 발생 시 raw.Metadata 에 MetadataKeyPartialLoad=true 가 채워져 있으며,
	// 해당 정보를 다운스트림 (validator/parser) 가 신뢰도 분기에 활용할 수 있도록 함.
	partialLoad := false
	if v, ok := raw.Metadata[MetadataKeyPartialLoad]; ok {
		if b, ok2 := v.(bool); ok2 && b {
			partialLoad = true
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
	if partialLoad {
		content.Extra[MetadataKeyPartialLoad] = true
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
		// 이슈 #146 + CodeRabbit 피드백: partial_load 인 경우 timeout 시점의 부분 DOM 이라
		// title/body 가 비어 있을 수 있다. "timeout = 정상 종료 시그널" 계약을 유지하기 위해
		// CDP_004 를 던지지 않고 빈 필드의 Content 를 그대로 반환 — 다운스트림 (validator)
		// 가 빈 필드 정책으로 처리. partial_load=false 인데 비어 있으면 진짜 파싱 실패라
		// 기존대로 CDP_004 에러 유지.
		if partialLoad {
			log.WithFields(map[string]interface{}{
				"url":          target.URL,
				"title_length": len(content.Title),
				"body_length":  len(content.Body),
			}).Warn("partial-load content missing required fields, returning as partial")
			return content, nil
		}
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
