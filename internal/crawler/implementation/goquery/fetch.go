package goquery

import (
	"context"
	"net/http"

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

	// HTTP 상태코드 검사: 4xx/5xx는 에러로 처리 (이슈 #75: core 공통 분기)
	if err := core.CheckHTTPStatus(target.URL, resp.StatusCode); err != nil {
		return nil, err
	}

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

	// Headers 추출: HTTP response.Header → map[string]string (다중값은 첫 항목만)
	headers := make(map[string]string, len(resp.Header))
	for key, values := range resp.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	// RawContent 조립 (이슈 #75: core 공통 생성자)
	rawContent := core.NewRawContent(c.name, c.sourceInfo, target, html, resp.StatusCode, headers)

	log.WithFields(map[string]interface{}{
		"url":         target.URL,
		"status_code": resp.StatusCode,
		"size":        len(html),
	}).Info("content fetched successfully with goquery")

	return rawContent, nil
}
