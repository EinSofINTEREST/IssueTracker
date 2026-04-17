package rate_limiter_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ratelimiter "issuetracker/internal/crawler/rate_limiter"
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
	for host, ip := range r.mapping {
		if strings.Contains(rawURL, host) {
			return ip, nil
		}
	}
	return "0.0.0.0", nil
}

// ─────────────────────────────────────────────────────────────────────────────
// IPRateLimiterRegistry 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestIPRateLimiterRegistry_Wait_AllowsWithinLimit(t *testing.T) {
	resolver := &staticIPResolver{ip: "1.2.3.4"}
	registry := ratelimiter.NewIPRateLimiterRegistry(resolver, 3600, 5)

	ctx := context.Background()

	for i := 0; i < 5; i++ {
		err := registry.Wait(ctx, "http://example.com/page")
		require.NoError(t, err)
	}
}

func TestIPRateLimiterRegistry_Wait_SameIPSharesLimiter(t *testing.T) {
	resolver := &staticIPResolver{ip: "10.0.0.1"}
	registry := ratelimiter.NewIPRateLimiterRegistry(resolver, 3600, 2)

	ctx := context.Background()

	err := registry.Wait(ctx, "http://domain-a.com/page")
	require.NoError(t, err)

	err = registry.Wait(ctx, "http://domain-b.com/page")
	require.NoError(t, err)

	// burst 소진 후 세 번째 요청은 대기 필요
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = registry.Wait(ctx, "http://domain-c.com/page")
	assert.Error(t, err)
}

func TestIPRateLimiterRegistry_Wait_DifferentIPsIndependent(t *testing.T) {
	resolver := &multiIPResolver{mapping: map[string]string{
		"site-a.com": "1.1.1.1",
		"site-b.com": "2.2.2.2",
	}}
	registry := ratelimiter.NewIPRateLimiterRegistry(resolver, 3600, 1)

	ctx := context.Background()

	err := registry.Wait(ctx, "http://site-a.com/page")
	require.NoError(t, err)

	err = registry.Wait(ctx, "http://site-b.com/page")
	require.NoError(t, err)
}

func TestIPRateLimiterRegistry_Wait_ContextCanceled(t *testing.T) {
	resolver := &staticIPResolver{ip: "5.5.5.5"}
	registry := ratelimiter.NewIPRateLimiterRegistry(resolver, 1, 1)

	ctx := context.Background()

	err := registry.Wait(ctx, "http://example.com/1")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = registry.Wait(ctx, "http://example.com/2")
	assert.Error(t, err)
}

func TestIPRateLimiterRegistry_Wait_DNSFailure_ProceedsWithoutLimit(t *testing.T) {
	resolver := &staticIPResolver{ip: "", err: assert.AnError}
	registry := ratelimiter.NewIPRateLimiterRegistry(resolver, 1, 1)

	var successCount int32
	for i := 0; i < 10; i++ {
		err := registry.Wait(context.Background(), "http://broken.com/page")
		if err == nil {
			atomic.AddInt32(&successCount, 1)
		}
	}

	assert.Equal(t, int32(10), successCount)
}
