package goquery

import (
	"context"
	"net/http"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html/charset"

	"issuetracker/internal/processor/fetcher/core"
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

	// charset.NewReader: Content-Type 헤더와 HTML meta 태그를 기반으로 charset 감지 후
	// 응답 body 를 UTF-8 스트림으로 변환한다 (이슈 #253 — EUC-KR 등 비UTF-8 인코딩 대응).
	// 변환 실패 시 WARN 로깅 후 원본 body 로 fallback — 하위 호환 유지.
	contentType := resp.Header.Get("Content-Type")
	utf8Reader, err := charset.NewReader(resp.Body, contentType)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"url":          target.URL,
			"content_type": contentType,
		}).WithError(err).Warn("charset detection failed, falling back to raw body")
		utf8Reader = resp.Body
	}

	// goquery Document 생성
	doc, err := goquery.NewDocumentFromReader(utf8Reader)
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
