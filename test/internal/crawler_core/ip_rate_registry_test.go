package core_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "issuetracker/internal/crawler/core"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock IPResolver
// ─────────────────────────────────────────────────────────────────────────────

type staticIPResolver struct {
	ip  string
	err error
}

func (r *staticIPResolver) Resolve(_ string) (string, error) {
	return r.ip, r.err
}

// multiIPResolver는 호스트별로 다른 IP를 반환합니다.
type multiIPResolver struct {
	mapping map[string]string
}

func (r *multiIPResolver) Resolve(rawURL string) (string, error) {
	// 간이 호스트 추출 (테스트용)
	for host, ip := range r.mapping {
		if len(rawURL) > 0 && contains(rawURL, host) {
			return ip, nil
		}
	}
	return "0.0.0.0", nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// IPRateLimiterRegistry 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestIPRateLimiterRegistry_Wait_AllowsWithinLimit(t *testing.T) {
	resolver := &staticIPResolver{ip: "1.2.3.4"}
	// 3600 requests/hour = 1 request/second, burst 5
	registry := core.NewIPRateLimiterRegistry(resolver, 3600, 5)

	ctx := context.Background()

	// burst 내의 요청은 즉시 허용
	for i := 0; i < 5; i++ {
		err := registry.Wait(ctx, "http://example.com/page")
		require.NoError(t, err)
	}
}

func TestIPRateLimiterRegistry_Wait_SameIPSharesLimiter(t *testing.T) {
	// 동일 IP로 해석되는 서로 다른 도메인은 하나의 limiter를 공유
	resolver := &staticIPResolver{ip: "10.0.0.1"}
	registry := core.NewIPRateLimiterRegistry(resolver, 3600, 2)

	ctx := context.Background()

	// burst 2: 두 요청은 즉시 허용
	err := registry.Wait(ctx, "http://domain-a.com/page")
	require.NoError(t, err)

	err = registry.Wait(ctx, "http://domain-b.com/page")
	require.NoError(t, err)

	// burst 소진 후 세 번째 요청은 대기 필요
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = registry.Wait(ctx, "http://domain-c.com/page")
	// timeout 내에 token이 생성되지 않으므로 context 에러 발생
	assert.Error(t, err)
}

func TestIPRateLimiterRegistry_Wait_DifferentIPsIndependent(t *testing.T) {
	resolver := &multiIPResolver{mapping: map[string]string{
		"site-a.com": "1.1.1.1",
		"site-b.com": "2.2.2.2",
	}}
	// burst 1: 각 IP당 1개만 즉시 허용
	registry := core.NewIPRateLimiterRegistry(resolver, 3600, 1)

	ctx := context.Background()

	// 서로 다른 IP이므로 각각 독립적으로 burst 1 사용 가능
	err := registry.Wait(ctx, "http://site-a.com/page")
	require.NoError(t, err)

	err = registry.Wait(ctx, "http://site-b.com/page")
	require.NoError(t, err)
}

func TestIPRateLimiterRegistry_Wait_ContextCanceled(t *testing.T) {
	resolver := &staticIPResolver{ip: "5.5.5.5"}
	registry := core.NewIPRateLimiterRegistry(resolver, 1, 1) // 1 req/hour

	ctx := context.Background()

	// burst 소진
	err := registry.Wait(ctx, "http://example.com/1")
	require.NoError(t, err)

	// context 취소 시 즉시 반환
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = registry.Wait(ctx, "http://example.com/2")
	assert.Error(t, err)
}

func TestIPRateLimiterRegistry_Wait_DNSFailure_ProceedsWithoutLimit(t *testing.T) {
	resolver := &staticIPResolver{ip: "", err: assert.AnError}
	registry := core.NewIPRateLimiterRegistry(resolver, 1, 1)

	// DNS 실패 시에도 에러 없이 진행 (graceful degradation)
	var successCount int32
	for i := 0; i < 10; i++ {
		err := registry.Wait(context.Background(), "http://broken.com/page")
		if err == nil {
			atomic.AddInt32(&successCount, 1)
		}
	}

	// DNS 실패 시 rate limiting 없이 모든 요청 허용
	assert.Equal(t, int32(10), successCount)
}
