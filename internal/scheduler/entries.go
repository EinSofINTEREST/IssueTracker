package scheduler

import (
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/config"
)

// sourceCategoryURLs 는 각 소스의 카테고리 이름 → URL 매핑입니다 (이슈 #247).
//
// 기존 sources/{kr,us}/*/config.go 에 하드코딩된 CategoryURLs 를 이 단일 상수로 통합.
// 스케줄러는 이 맵에서 ScheduleEntry 를 생성하고, ChainHandler 는 fetcher_rules DB 에서
// SourceInfo·RequestsPerHour 를 조회하므로 각자의 관심사가 분리됩니다.
var sourceCategoryURLs = map[string]map[string]string{
	"naver": {
		"politics": "https://news.naver.com/section/100",
		"economy":  "https://news.naver.com/section/101",
		"society":  "https://news.naver.com/section/102",
		"culture":  "https://news.naver.com/section/103",
		"world":    "https://news.naver.com/section/104",
		"IT":       "https://news.naver.com/section/105",
	},
	"daum": {
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
	"yonhap": {
		"politics": "https://www.yna.co.kr/politics/all",
		"economy":  "https://www.yna.co.kr/economy/all",
		"society":  "https://www.yna.co.kr/society/all",
		"culture":  "https://www.yna.co.kr/culture/all",
		"world":    "https://www.yna.co.kr/international/all",
	},
	"cnn": {
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
}

// DefaultEntries는 현재 등록된 모든 소스의 기본 ScheduleEntry 목록을 반환합니다.
//
// DefaultEntries builds the full list of ScheduleEntry values from sourceCategoryURLs.
// Intervals are controlled by SchedulerConfig.
func DefaultEntries(cfg config.SchedulerConfig) []ScheduleEntry {
	var entries []ScheduleEntry
	for crawlerName, categoryURLs := range sourceCategoryURLs {
		for _, url := range categoryURLs {
			entries = append(entries, ScheduleEntry{
				CrawlerName: crawlerName,
				URL:         url,
				TargetType:  core.TargetTypeCategory,
				Interval:    cfg.CategoryInterval,
				Priority:    core.PriorityNormal,
				Timeout:     cfg.JobTimeout,
			})
		}
	}
	return entries
}
