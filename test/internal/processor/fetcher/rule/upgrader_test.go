package rule_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	fetcherRule "issuetracker/internal/processor/fetcher/rule"
)

// TestNewUpgrader_NilDependencies_ReturnsError:
// 이슈 #208 panic-on-nil → error 정책. 모든 필수 의존성이 nil 이면 즉시 error.
//
// 의존성 stub 만들기 비용이 크므로 (FetcherRuleRepository / Resolver / RawIDTracker /
// RawContentService / Producer 모두 인터페이스) 핵심 invariant — 개별 nil 검증 — 만 검사.
// 전체 흐름 검증은 통합 테스트 영역.
func TestNewUpgrader_NilDependencies_ReturnsError(t *testing.T) {
	// 모두 nil
	_, err := fetcherRule.NewUpgrader(nil, nil, nil, nil, nil, nil, nil, nil)
	assert.Error(t, err)
}

// TestUpgrader_Trigger_NilReceiver_NoPanic:
// nil Upgrader 에 Trigger 호출해도 panic 없음 — wiring 실패 시 graceful 보장.
func TestUpgrader_Trigger_NilReceiver_NoPanic(t *testing.T) {
	var u *fetcherRule.Upgrader
	assert.NotPanics(t, func() {
		u.Trigger(context.Background(), "host.com")
	})
}
