// Package yonhap는 연합뉴스 크롤러를 구현합니다.
//
// Package yonhap implements a crawler for Yonhap News Agency (yna.co.kr).
// Strategy: HTML scraping via goquery.
package yonhap

import (
	"issuetracker/internal/crawler/core"
)

// YonhapConfig는 연합뉴스 크롤러 설정입니다.
type YonhapConfig struct {
	// BaseURL은 연합뉴스 메인 URL입니다.
	BaseURL string

	// ArticleSelectors는 기사 페이지 파싱에 사용하는 CSS 셀렉터 맵입니다.
	ArticleSelectors map[string]string

	// ListSelectors는 카테고리 목록 페이지 CSS 셀렉터 맵입니다.
	ListSelectors map[string]string

	// CrawlerConfig는 공통 크롤러 설정입니다.
	CrawlerConfig core.Config
}

// DefaultYonhapConfig는 연합뉴스 기본 설정을 반환합니다.
func DefaultYonhapConfig() YonhapConfig {
	cfg := core.DefaultConfig()
	cfg.RequestsPerHour = 100
	cfg.SourceInfo = core.SourceInfo{
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "yonhap",
		BaseURL:  "https://www.yna.co.kr",
		Language: "ko",
	}

	return YonhapConfig{
		BaseURL: "https://www.yna.co.kr",
		ArticleSelectors: map[string]string{
			// 제목: <h1 class="tit01">
			"title": "h1.tit01",
			// 본문 래퍼: <div class="story-news article"> — 하위 <p> 태그를 추출
			"body": ".story-news.article",
			// 작성자 영역: <div id="newsWriterCarousel01" class="writer-zone01">
			// 실제 이름은 하위 div > div > div > div > strong 에서 추출 (다수 가능)
			"author": "#newsWriterCarousel01",
			// 발행일시
			"date": ".update-time",
			// 태그 영역: <div class="keyword-zone"> — 하위 div > ul > li > a
			"tags": ".keyword-zone",
			// 이미지 영역: <div class="comp-box photo-group"> — 하위 figure > div > span > img
			"images": ".comp-box.photo-group",
		},
		ListSelectors: map[string]string{
			"item":    ".alist-item",
			"title":   ".alist-item-txt",
			"link":    "a",
			"summary": ".lead",
		},
		CrawlerConfig: cfg,
	}
}
