// Package cnn implements a crawler for CNN (www.cnn.com).
//
// Package cnn은 CNN 뉴스 크롤러를 구현합니다.
// Primary strategy: HTML scraping via goquery (for category lists and full article content).
// Fallback strategy: headless browser via chromedp (for JavaScript-rendered pages).
package cnn

import (
	"issuetracker/internal/crawler/core"
)

// CNNConfig는 CNN 뉴스 크롤러 설정입니다.
type CNNConfig struct {
	// BaseURL은 CNN 메인 URL입니다.
	BaseURL string

	// CategoryURLs는 카테고리명 → HTML 목록 페이지 URL 매핑입니다.
	CategoryURLs map[string]string

	// ArticleSelectors는 기사 페이지 파싱에 사용하는 CSS 셀렉터 맵입니다.
	ArticleSelectors map[string]string

	// ListSelectors는 목록 페이지 파싱에 사용하는 CSS 셀렉터 맵입니다.
	ListSelectors map[string]string

	// CrawlerConfig는 공통 크롤러 설정입니다.
	CrawlerConfig core.Config
}

// DefaultCNNConfig는 CNN 기본 설정을 반환합니다.
func DefaultCNNConfig() CNNConfig {
	cfg := core.DefaultConfig()
	cfg.RequestsPerHour = 100
	cfg.SourceInfo = core.SourceInfo{
		Country:  "US",
		Type:     core.SourceTypeNews,
		Name:     "cnn",
		BaseURL:  "https://www.cnn.com",
		Language: "en",
	}

	return CNNConfig{
		BaseURL: "https://www.cnn.com",
		CategoryURLs: map[string]string{
			"top":           "https://edition.cnn.com",
			"us":            "https://edition.cnn.com/us",
			"world":         "https://edition.cnn.com/world",
			"politics":      "https://edition.cnn.com/politics",
			"business":      "https://edition.cnn.com/business",
			"tech":          "https://edition.cnn.com/business/tech",
			"health":        "https://edition.cnn.com/health",
			"entertainment": "https://edition.cnn.com/entertainment",
			"sports":        "https://edition.cnn.com/sport",
		},
		ArticleSelectors: map[string]string{
			// 제목: h1.headline__text → h1 (폴백)
			"title": "h1.headline__text",
			// 본문 컨테이너: .article__content
			"body": "div.article__content",
			// 저자: .byline__name (복수 가능)
			"author": "span.byline__name",
			// 날짜: .timestamp 텍스트 (예: "Updated 3:30 PM EST, Mon March 3, 2026")
			"date": "div.timestamp",
			// 카테고리: 브레드크럼 첫 번째 링크
			"category": "ol.breadcrumb li:first-child a",
			// 태그: 메타데이터 태그라인
			"tags": "div.metadata__tagline a",
			// 이미지: 본문 영역 내 img
			"image_urls": "div.article__content img",
		},
		ListSelectors: map[string]string{
			"item":    "div.container__item",
			"link":    "a.container__link",
			"title":   "span.container__headline-text",
			"summary": "div.container__description",
		},
		CrawlerConfig: cfg,
	}
}
