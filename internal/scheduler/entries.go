package scheduler

import (
	"net/url"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/categories"
	"issuetracker/pkg/config"
)

// categoryURL 은 (URL, category) 쌍으로 scheduler entry 의 입력입니다 (이슈 #381).
//
// 기존 string slice 에서 category 메타데이터를 함께 보존하도록 확장. category 는 pkg/categories
// enum 으로 단일화되어 다운스트림 CategoryBasedResolver 가 priority 결정에 활용.
type categoryURL struct {
	URL      string
	Category categories.Category
}

// sourceCategoryURLs 는 각 소스의 카테고리 URL 목록입니다.
//
// 기존 sources/{kr,us}/*/config.go 에 하드코딩된 CategoryURLs 를 이 맵으로 통합.
// DefaultEntries 가 이 목록에서 ScheduleEntry 를 생성합니다.
//
// 본 맵은 이슈 #328 부터 fallback 전용 — DB 우선 (scheduler_entries 테이블) 정책.
// scheduler_entries seed 는 migrations/up/019_scheduler_entries.sql 참조. interval 은
// migration 020 부터 30m 단축 적용 — 본 fallback 은 cfg.CategoryInterval (env) 사용.
var sourceCategoryURLs = map[string][]categoryURL{
	"naver": {
		{"https://news.naver.com/section/100", categories.CategoryPolitics},
		{"https://news.naver.com/section/101", categories.CategoryEconomy},
		{"https://news.naver.com/section/102", categories.CategorySociety},
		{"https://news.naver.com/section/103", categories.CategoryCulture},
		{"https://news.naver.com/section/104", categories.CategoryInternational},
		{"https://news.naver.com/section/105", categories.CategoryTech},
	},
	"daum": {
		{"https://news.daum.net/politics", categories.CategoryPolitics},
		{"https://news.daum.net/economic", categories.CategoryEconomy},
		{"https://news.daum.net/society", categories.CategorySociety},
		{"https://news.daum.net/culture", categories.CategoryCulture},
		{"https://news.daum.net/foreign", categories.CategoryInternational},
		{"https://news.daum.net/tech", categories.CategoryTech},
		{"https://news.daum.net/climate", categories.CategoryClimate},
		{"https://news.daum.net/life", categories.CategoryLifestyle},
		{"https://news.daum.net/understanding", categories.CategoryColumn},
	},
	"yonhap": {
		{"https://www.yna.co.kr/politics/all", categories.CategoryPolitics},
		{"https://www.yna.co.kr/economy/all", categories.CategoryEconomy},
		{"https://www.yna.co.kr/society/all", categories.CategorySociety},
		{"https://www.yna.co.kr/culture/all", categories.CategoryCulture},
		{"https://www.yna.co.kr/international/all", categories.CategoryInternational},
	},
	"cnn": {
		{"https://edition.cnn.com", categories.CategoryBreakingNews},
		{"https://edition.cnn.com/us", categories.CategorySociety},
		{"https://edition.cnn.com/world", categories.CategoryInternational},
		{"https://edition.cnn.com/politics", categories.CategoryPolitics},
		{"https://edition.cnn.com/business", categories.CategoryBusiness},
		{"https://edition.cnn.com/business/tech", categories.CategoryTech},
		{"https://edition.cnn.com/health", categories.CategoryHealth},
		{"https://edition.cnn.com/entertainment", categories.CategoryEntertainment},
		{"https://edition.cnn.com/sport", categories.CategorySports},
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
func DefaultEntries(cfg config.SchedulerConfig) []ScheduleEntry {
	var entries []ScheduleEntry
	for _, urls := range sourceCategoryURLs {
		for _, cu := range urls {
			host := hostOf(cu.URL)
			entries = append(entries, ScheduleEntry{
				CrawlerName: host,
				URL:         cu.URL,
				TargetType:  core.TargetTypeCategory,
				Interval:    cfg.CategoryInterval,
				Priority:    core.PriorityNormal,
				Category:    cu.Category,
				Timeout:     cfg.JobTimeout,
			})
		}
	}
	return entries
}
