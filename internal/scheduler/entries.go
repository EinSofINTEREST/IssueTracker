package scheduler

import (
	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/general/sources/kr/daum"
	"issuetracker/internal/crawler/domain/general/sources/kr/naver"
	"issuetracker/internal/crawler/domain/general/sources/kr/yonhap"
	"issuetracker/internal/crawler/domain/general/sources/us/cnn"
	"issuetracker/pkg/config"
)

// DefaultEntries는 현재 등록된 모든 소스의 기본 ScheduleEntry 목록을 반환합니다.
//
// DefaultEntries builds the full list of ScheduleEntry values from each
// source's default config. Intervals are controlled by SchedulerConfig.
func DefaultEntries(cfg config.SchedulerConfig) []ScheduleEntry {
	var entries []ScheduleEntry
	entries = append(entries, cnnEntries(cfg)...)
	entries = append(entries, naverEntries(cfg)...)
	entries = append(entries, yonhapEntries(cfg)...)
	entries = append(entries, daumEntries(cfg)...)
	return entries
}

// cnnEntries는 CNN 카테고리 페이지 기반 스케줄 항목을 반환합니다.
// CNN RSS 피드가 지원 중단되어 HTML 카테고리 페이지를 직접 크롤링합니다.
func cnnEntries(cfg config.SchedulerConfig) []ScheduleEntry {
	cnnCfg := cnn.Default()
	entries := make([]ScheduleEntry, 0, len(cnnCfg.CategoryURLs))

	for _, url := range cnnCfg.CategoryURLs {
		entries = append(entries, ScheduleEntry{
			CrawlerName: "cnn",
			URL:         url,
			TargetType:  core.TargetTypeCategory,
			Interval:    cfg.CategoryInterval,
			Priority:    core.PriorityNormal,
			Timeout:     cfg.JobTimeout,
		})
	}
	return entries
}

// naverEntries는 네이버 뉴스 카테고리 페이지 기반 스케줄 항목을 반환합니다.
func naverEntries(cfg config.SchedulerConfig) []ScheduleEntry {
	naverCfg := naver.Default()
	entries := make([]ScheduleEntry, 0, len(naverCfg.CategoryURLs))

	for _, url := range naverCfg.CategoryURLs {
		entries = append(entries, ScheduleEntry{
			CrawlerName: "naver",
			URL:         url,
			TargetType:  core.TargetTypeCategory,
			Interval:    cfg.CategoryInterval,
			Priority:    core.PriorityNormal,
			Timeout:     cfg.JobTimeout,
		})
	}
	return entries
}

// yonhapEntries는 연합뉴스 기반 스케줄 항목을 반환합니다.
// 연합뉴스는 RSS 미지원, 카테고리 URL을 직접 사용합니다.
func yonhapEntries(cfg config.SchedulerConfig) []ScheduleEntry {
	yonhapCfg := yonhap.Default()
	entries := make([]ScheduleEntry, 0, len(yonhapCfg.CategoryURLs))

	for _, url := range yonhapCfg.CategoryURLs {
		entries = append(entries, ScheduleEntry{
			CrawlerName: "yonhap",
			URL:         url,
			TargetType:  core.TargetTypeCategory,
			Interval:    cfg.CategoryInterval,
			Priority:    core.PriorityNormal,
			Timeout:     cfg.JobTimeout,
		})
	}
	return entries
}

// daumEntries는 다음 뉴스 카테고리 페이지 기반 스케줄 항목을 반환합니다.
func daumEntries(cfg config.SchedulerConfig) []ScheduleEntry {
	daumCfg := daum.Default()
	entries := make([]ScheduleEntry, 0, len(daumCfg.CategoryURLs))

	for _, url := range daumCfg.CategoryURLs {
		entries = append(entries, ScheduleEntry{
			CrawlerName: "daum",
			URL:         url,
			TargetType:  core.TargetTypeCategory,
			Interval:    cfg.CategoryInterval,
			Priority:    core.PriorityNormal,
			Timeout:     cfg.JobTimeout,
		})
	}
	return entries
}
