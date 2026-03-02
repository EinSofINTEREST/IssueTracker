// Package naver는 네이버 뉴스 크롤러를 구현합니다.
//
// Package naver implements a crawler for Naver News (news.naver.com).
// Primary strategy: HTML scraping via goquery.
// Fallback strategy: headless browser via chromedp.
package naver

import (
	"issuetracker/internal/crawler/core"
)

// NaverConfig는 네이버 뉴스 크롤러 설정입니다.
type NaverConfig struct {
	// BaseURL은 네이버 뉴스 메인 URL입니다.
	BaseURL string

	// CategoryURLs는 카테고리명 → URL 매핑입니다.
	CategoryURLs map[string]string

	// ArticleSelectors는 기사 페이지 파싱에 사용하는 CSS 셀렉터 맵입니다.
	ArticleSelectors map[string]string

	// ListSelectors는 목록 페이지 파싱에 사용하는 CSS 셀렉터 맵입니다.
	ListSelectors map[string]string

	// CrawlerConfig는 공통 크롤러 설정입니다.
	CrawlerConfig core.Config
}

// DefaultNaverConfig는 네이버 뉴스 기본 설정을 반환합니다.
func DefaultNaverConfig() NaverConfig {
	cfg := core.DefaultConfig()
	cfg.RequestsPerHour = 200
	cfg.SourceInfo = core.SourceInfo{
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "naver",
		BaseURL:  "https://news.naver.com",
		Language: "ko",
	}

	return NaverConfig{
		BaseURL: "https://news.naver.com",
		CategoryURLs: map[string]string{
			"politics": "https://news.naver.com/section/100",
			"economy":  "https://news.naver.com/section/101",
			"society":  "https://news.naver.com/section/102",
			"culture":  "https://news.naver.com/section/103",
			"world":    "https://news.naver.com/section/104",
			"IT":       "https://news.naver.com/section/105",
		},
		ArticleSelectors: map[string]string{
			"title":      "#title_area span",
			"body":       "#dic_area",
			"author":     ".media_end_head_journalist_name",
			"date":       ".media_end_head_info_datestamp_time",
			"category":   "ul.Nlnb_menu_list li.is_active a span",
			"image_urls": "span.end_photo_org",
		},
		ListSelectors: map[string]string{
			"item":    ".sa_item",
			"title":   ".sa_text_title",
			"link":    "a.sa_text_title",
			"summary": ".sa_text_lede",
		},
		CrawlerConfig: cfg,
	}
}
