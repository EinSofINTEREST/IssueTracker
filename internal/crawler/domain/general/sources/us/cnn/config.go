// Package cnn 는 CNN 뉴스 크롤러 설정을 제공합니다.
//
// Package cnn provides CNN crawler configuration.
// 파싱 규칙은 parsing_rules 테이블 (host_pattern='edition.cnn.com') 에 보관.
package cnn

import (
	"issuetracker/internal/crawler/core"
)

type Config struct {
	BaseURL       string
	CategoryURLs  map[string]string
	CrawlerConfig core.Config
}

func Default() Config {
	cfg := core.DefaultConfig()
	cfg.RequestsPerHour = 100
	cfg.SourceInfo = core.SourceInfo{
		Country:  "US",
		Type:     core.SourceTypeNews,
		Name:     "cnn",
		BaseURL:  "https://www.cnn.com",
		Language: "en",
	}
	return Config{
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
		CrawlerConfig: cfg,
	}
}
