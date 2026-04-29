// Package yonhap 는 연합뉴스 크롤러 설정을 제공합니다.
//
// Package yonhap provides Yonhap News crawler configuration.
// 파싱 규칙은 parsing_rules 테이블 (host_pattern='www.yna.co.kr') 에 보관.
package yonhap

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
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "yonhap",
		BaseURL:  "https://www.yna.co.kr",
		Language: "ko",
	}
	return Config{
		BaseURL: "https://www.yna.co.kr",
		CategoryURLs: map[string]string{
			"politics": "https://www.yna.co.kr/politics/all",
			"economy":  "https://www.yna.co.kr/economy/all",
			"society":  "https://www.yna.co.kr/society/all",
			"culture":  "https://www.yna.co.kr/culture/all",
			"world":    "https://www.yna.co.kr/international/all",
		},
		CrawlerConfig: cfg,
	}
}
