// Package daum 는 다음 뉴스 크롤러 설정을 제공합니다.
//
// Package daum provides Daum News crawler configuration.
// 파싱 규칙은 parsing_rules 테이블 (host_pattern='v.daum.net'/'news.daum.net') 에 보관.
package daum

import (
	"issuetracker/internal/crawler/core"
)

type Config struct {
	BaseURL        string
	ArticleBaseURL string
	CategoryURLs   map[string]string
	CrawlerConfig  core.Config
}

func Default() Config {
	cfg := core.DefaultConfig()
	cfg.RequestsPerHour = 200
	cfg.SourceInfo = core.SourceInfo{
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "daum",
		BaseURL:  "https://news.daum.net",
		Language: "ko",
	}
	return Config{
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
		CrawlerConfig: cfg,
	}
}
