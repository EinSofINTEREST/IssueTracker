package goquery

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// Fetch: URL에서 컨텐츠 가져오기
func (c *GoqueryCrawler) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	log := logger.FromContext(ctx)

	// HTTP 요청
	req, err := http.NewRequestWithContext(ctx, "GET", target.URL, nil)
	if err != nil {
		return nil, &core.CrawlerError{
			Category:  core.ErrCategoryInternal,
			Code:      "REQ_001",
			Message:   "failed to create request",
			Source:    c.name,
			URL:       target.URL,
			Retryable: false,
			Err:       err,
		}
	}

	req.Header.Set("User-Agent", c.config.UserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &core.CrawlerError{
			Category:  core.ErrCategoryNetwork,
			Code:      "NET_001",
			Message:   "failed to fetch URL",
			Source:    c.name,
			URL:       target.URL,
			Retryable: true,
			Err:       err,
		}
	}
	defer resp.Body.Close()

	// goquery Document 생성
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, &core.CrawlerError{
			Category: core.ErrCategoryParse,
			Code:     "PARSE_001",
			Message:  "failed to parse HTML",
			Source:   c.name,
			URL:      target.URL,
			Err:      err,
		}
	}

	// HTML 저장 (Document에서 추출)
	html, err := doc.Html()
	if err != nil {
		return nil, &core.CrawlerError{
			Category: core.ErrCategoryParse,
			Code:     "PARSE_002",
			Message:  "failed to extract HTML",
			Source:   c.name,
			URL:      target.URL,
			Err:      err,
		}
	}

	rawContent := &core.RawContent{
		ID:         fmt.Sprintf("%s-%d", c.name, time.Now().UnixNano()),
		SourceInfo: c.sourceInfo,
		FetchedAt:  time.Now(),
		URL:        target.URL,
		HTML:       html,
		StatusCode: resp.StatusCode,
		Headers:    make(map[string]string),
		Metadata:   target.Metadata,
	}

	// Headers 저장
	for key, values := range resp.Header {
		if len(values) > 0 {
			rawContent.Headers[key] = values[0]
		}
	}

	log.WithFields(map[string]interface{}{
		"url":         target.URL,
		"status_code": resp.StatusCode,
		"size":        len(html),
	}).Info("content fetched successfully with goquery")

	return rawContent, nil
}
