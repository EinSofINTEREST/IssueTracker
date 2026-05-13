package worker_test

// helpers_test.go — worker 패키지 테스트 공통 fixture 빌더.
//
// 이슈 #417 — validate worker 가 sub-package 로 분리되면서 기존 validate_test 의 helper
// (newNewsContent / newCommunityContent) 가 cross-package 접근 불가. 동일 fixture 를 worker
// 테스트에서도 사용하기 위해 본 파일에 사본 보유. 원본은 test/internal/processor/validate/
// 의 community_validator_test.go / news_validator_test.go 에 잔존 — sub-validator 자체 단위
// 테스트와 worker integration 테스트는 독립적으로 유지.

import (
	"strings"
	"time"

	"issuetracker/internal/processor/fetcher/core"
)

// testPublishedAt 은 결정성 (determinism) 을 위해 fixture 가 공유하는 고정 timestamp 입니다 —
// coderabbit PR #418 피드백. time.Now() 사용 시 시간-민감한 validation 분기에서 flaky test
// 위험.
var testPublishedAt = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

func newNewsContent() *core.Content {
	return &core.Content{
		ID:          "content-001",
		SourceID:    "cnn",
		SourceType:  core.SourceTypeNews,
		Country:     "US",
		Language:    "en",
		Title:       "Breaking News: Major Event Occurs in Capital City",
		Body:        strings.Repeat("This is a test article body sentence. ", 10),
		Summary:     "Short summary of the article.",
		Author:      "Jane Doe",
		PublishedAt: testPublishedAt,
		Category:    "Politics",
		Tags:        []string{"news", "politics"},
		URL:         "https://cnn.com/article/123",
		WordCount:   80,
	}
}

func newCommunityContent() *core.Content {
	return &core.Content{
		ID:          "content-002",
		SourceID:    "reddit",
		SourceType:  core.SourceTypeCommunity,
		Country:     "US",
		Language:    "en",
		Title:       "Community post about something",
		Body:        strings.Repeat("This is a community post body sentence. ", 5),
		Author:      "u/testuser",
		PublishedAt: testPublishedAt,
		Tags:        []string{"discussion"},
		URL:         "https://reddit.com/r/test/comments/abc",
		WordCount:   40,
	}
}
