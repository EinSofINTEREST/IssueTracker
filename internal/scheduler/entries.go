package scheduler

import (
	processorcfg "issuetracker/pkg/config/processor"
	"net/url"

	"issuetracker/internal/processor/fetcher/core"
)

// sourceCategoryURLs 는 각 소스의 카테고리 URL 목록입니다.
//
// 기존 sources/{kr,us}/*/config.go 에 하드코딩된 CategoryURLs 를 이 맵으로 통합.
// DefaultEntries 가 이 목록에서 ScheduleEntry 를 생성합니다.
// 카테고리 이름 키는 사용하지 않으므로 map[string][]string 으로 URL 목록만 보관합니다.
//
// 본 맵은 이슈 #328 부터 fallback 전용 — DB 우선 (scheduler_entries 테이블) 정책.
// scheduler_entries seed 는 migrations/up/019_scheduler_entries.sql 참조. interval 은
// migration 020 부터 30m 단축 적용 — 본 fallback 은 cfg.CategoryInterval (env) 사용.
var sourceCategoryURLs = map[string][]string{
	"naver": {
		"https://news.naver.com/section/100", // politics
		"https://news.naver.com/section/101", // economy
		"https://news.naver.com/section/102", // society
		"https://news.naver.com/section/103", // culture
		"https://news.naver.com/section/104", // world
		"https://news.naver.com/section/105", // it
	},
	"daum": {
		"https://news.daum.net/politics",      // politics
		"https://news.daum.net/economic",      // economy
		"https://news.daum.net/society",       // society
		"https://news.daum.net/culture",       // culture
		"https://news.daum.net/foreign",       // world
		"https://news.daum.net/tech",          // it
		"https://news.daum.net/climate",       // climate
		"https://news.daum.net/life",          // life
		"https://news.daum.net/understanding", // column
	},
	"yonhap": {
		"https://www.yna.co.kr/politics/all",      // politics
		"https://www.yna.co.kr/economy/all",       // economy
		"https://www.yna.co.kr/society/all",       // society
		"https://www.yna.co.kr/culture/all",       // culture
		"https://www.yna.co.kr/international/all", // world
	},
	"cnn": {
		"https://edition.cnn.com",               // top
		"https://edition.cnn.com/us",            // us
		"https://edition.cnn.com/world",         // world
		"https://edition.cnn.com/politics",      // politics
		"https://edition.cnn.com/business",      // business
		"https://edition.cnn.com/business/tech", // tech
		"https://edition.cnn.com/health",        // health
		"https://edition.cnn.com/entertainment", // entertainment
		"https://edition.cnn.com/sport",         // sports
	},
}

// hostOf 는 rawURL 에서 hostname 을 추출합니다. 파싱 실패 시 rawURL 을 그대로 반환합니다.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return rawURL
	}
	return u.Hostname()
}

// DefaultEntries는 현재 등록된 모든 소스의 기본 ScheduleEntry 목록을 반환합니다.
//
// DefaultEntries builds the full list of ScheduleEntry values from sourceCategoryURLs.
// Intervals are controlled by SchedulerConfig.
func DefaultEntries(cfg processorcfg.SchedulerConfig) []ScheduleEntry {
	var entries []ScheduleEntry
	for _, urls := range sourceCategoryURLs {
		for _, rawURL := range urls {
			host := hostOf(rawURL)
			entries = append(entries, ScheduleEntry{
				CrawlerName: host,
				URL:         rawURL,
				TargetType:  core.TargetTypeCategory,
				Interval:    cfg.CategoryInterval,
				Priority:    core.PriorityNormal,
				Timeout:     cfg.JobTimeout,
			})
		}
	}
	return entries
}
