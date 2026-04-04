// Package daum는 다음(Daum) 뉴스 크롤러를 구현합니다.
//
// Package daum implements a crawler for Daum News (news.daum.net / v.daum.net).
// Primary strategy: HTML scraping via goquery.
// Fallback strategy: headless browser via chromedp.
package daum

import (
	"issuetracker/internal/crawler/core"
)

// DaumConfig는 다음 뉴스 크롤러 설정입니다.
type DaumConfig struct {
	// BaseURL은 다음 뉴스 메인 URL입니다.
	BaseURL string

	// ArticleBaseURL은 다음 뉴스 기사 페이지 기본 URL입니다.
	ArticleBaseURL string

	// CategoryURLs는 카테고리명 → URL 매핑입니다.
	CategoryURLs map[string]string

	// ArticleSelectors는 기사 페이지 파싱에 사용하는 CSS 셀렉터 맵입니다.
	ArticleSelectors map[string]string

	// ListSelectors는 목록 페이지 파싱에 사용하는 CSS 셀렉터 맵입니다.
	ListSelectors map[string]string

	// CrawlerConfig는 공통 크롤러 설정입니다.
	CrawlerConfig core.Config
}

// DefaultDaumConfig는 다음 뉴스 기본 설정을 반환합니다.
func DefaultDaumConfig() DaumConfig {
	cfg := core.DefaultConfig()
	cfg.RequestsPerHour = 200
	cfg.SourceInfo = core.SourceInfo{
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "daum",
		BaseURL:  "https://news.daum.net",
		Language: "ko",
	}

	return DaumConfig{
		BaseURL:        "https://news.daum.net",
		ArticleBaseURL: "https://v.daum.net",
		CategoryURLs: map[string]string{
			"politics": "https://news.daum.net/politics",
			"economy":  "https://news.daum.net/economic",
			"society":  "https://news.daum.net/society",
			"culture":  "https://news.daum.net/culture",
			"world":    "https://news.daum.net/foreign",
			"IT":       "https://news.daum.net/tech",
			"climate":  "https://news.daum.net/climate",
			"life":     "https://news.daum.net/life",
			"column":   "https://news.daum.net/understanding",
		},
		ArticleSelectors: map[string]string{
			"title":      "h3.tit_view",
			"body":       ".article_view",
			"author":     "span.txt_info",
			"date":       "span.txt_info",
			"category":   ".info_cate",
			"tags":       ".keyword_area a",
			"image_urls": ".article_view img",
		},
		ListSelectors: map[string]string{
			"item":    ".item_issue",
			"link":    "a.link_txt",
			"summary": ".desc_txt",
		},
		CrawlerConfig: cfg,
	}
}
