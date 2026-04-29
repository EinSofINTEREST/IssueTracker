// Package naver 는 네이버 뉴스 크롤러 설정을 제공합니다.
//
// Package naver provides Naver News crawler configuration.
// 파싱 규칙은 parsing_rules 테이블 (host_pattern='n.news.naver.com'/'news.naver.com') 에 보관 — 본 패키지는 source 메타와 카테고리 URL 만.
package naver

import (
	"issuetracker/internal/crawler/core"
)

// Config 는 네이버 뉴스 크롤러 설정입니다.
// 사이트별 selector 는 parsing_rules 테이블이 보유 — 본 struct 는 source 메타 + 카테고리 URL 만.
type Config struct {
	BaseURL       string
	CategoryURLs  map[string]string
	CrawlerConfig core.Config
}

// Default 는 네이버 뉴스 기본 설정을 반환합니다.
func Default() Config {
	cfg := core.DefaultConfig()
	cfg.RequestsPerHour = 200
	cfg.SourceInfo = core.SourceInfo{
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "naver",
		BaseURL:  "https://news.naver.com",
		Language: "ko",
	}
	return Config{
		BaseURL: "https://news.naver.com",
		CategoryURLs: map[string]string{
			"politics": "https://news.naver.com/section/100",
			"economy":  "https://news.naver.com/section/101",
			"society":  "https://news.naver.com/section/102",
			"culture":  "https://news.naver.com/section/103",
			"world":    "https://news.naver.com/section/104",
			"IT":       "https://news.naver.com/section/105",
		},
		CrawlerConfig: cfg,
	}
}
