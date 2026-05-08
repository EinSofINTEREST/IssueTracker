package rate_limiter_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ratelimiter "issuetracker/internal/processor/fetcher/rate_limiter"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock IPResolver
// ─────────────────────────────────────────────────────────────────────────────

type staticIPResolver struct {
	ip  string
	err error
}

func (r *staticIPResolver) Resolve(_ context.Context, _ string) (string, error) {
	return r.ip, r.err
}

// multiIPResolver는 호스트별로 다른 IP를 반환합니다.
type multiIPResolver struct {
	mapping map[string]string
}

func (r *multiIPResolver) Resolve(_ context.Context, rawURL string) (string, error) {
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

func TestIPRateLimiterRegistry_Wait_DNSFailure_ContextCanceled_ReturnsCtxErr(t *testing.T) {
	resolver := &staticIPResolver{ip: "", err: assert.AnError}
	registry := ratelimiter.NewIPRateLimiterRegistry(resolver, 1, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := registry.Wait(ctx, "http://broken.com/page")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestNewRateLimiter_ZeroRequestsPerHour_ReturnsNoopLimiter(t *testing.T) {
	limiter := ratelimiter.NewRateLimiter(0, 10)

	// 0 이하 RequestsPerHour는 noop limiter를 반환 (모든 요청 허용)
	for i := 0; i < 100; i++ {
		assert.True(t, limiter.Allow())
	}
	assert.NoError(t, limiter.Wait(context.Background()))
}

// ─────────────────────────────────────────────────────────────────────────────
// resolver 모드 (#322 — dynamic RPH)
// ─────────────────────────────────────────────────────────────────────────────

// stubSourceConfigResolver 는 host 별 SourceConfig 를 in-memory 로 반환합니다.
type stubSourceConfigResolver struct {
	mu    sync.Mutex
	cfgs  map[string]ratelimiter.SourceConfig
	calls int
}

func (r *stubSourceConfigResolver) Resolve(_ context.Context, host string) (ratelimiter.SourceConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if cfg, ok := r.cfgs[host]; ok {
		return cfg, nil
	}
	return ratelimiter.SourceConfig{}, nil
}

func (r *stubSourceConfigResolver) Invalidate(_ string) {}

func (r *stubSourceConfigResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestIPRateLimiterRegistryWithResolver_NewLimiter_LookupsHostRPH(t *testing.T) {
	resolver := &staticIPResolver{ip: "8.8.8.8"}
	configResolver := &stubSourceConfigResolver{
		cfgs: map[string]ratelimiter.SourceConfig{
			"high.example.com": {RequestsPerHour: 3600}, // 1/sec — burst 1 이면 두번째도 즉시 통과 가능
		},
	}
	registry := ratelimiter.NewIPRateLimiterRegistryWithResolver(resolver, configResolver, 1)

	err := registry.Wait(context.Background(), "http://high.example.com/p1")
	require.NoError(t, err)
	assert.Equal(t, 1, configResolver.callCount(), "limiter 생성 시 resolver 1회 lookup")

	// 같은 IP 의 두 번째 호출 — 기존 limiter 재사용, 추가 lookup 없음.
	err = registry.Wait(context.Background(), "http://high.example.com/p2")
	require.NoError(t, err)
	assert.Equal(t, 1, configResolver.callCount(), "기존 limiter 재사용으로 lookup 추가 발생 X")
}

func TestIPRateLimiterRegistryWithResolver_DifferentIPs_LookupPerIP(t *testing.T) {
	resolver := &multiIPResolver{mapping: map[string]string{
		"site-a.com": "1.1.1.1",
		"site-b.com": "2.2.2.2",
	}}
	configResolver := &stubSourceConfigResolver{
		cfgs: map[string]ratelimiter.SourceConfig{
			"site-a.com": {RequestsPerHour: 3600},
			"site-b.com": {RequestsPerHour: 3600},
		},
	}
	registry := ratelimiter.NewIPRateLimiterRegistryWithResolver(resolver, configResolver, 1)

	require.NoError(t, registry.Wait(context.Background(), "http://site-a.com/p"))
	require.NoError(t, registry.Wait(context.Background(), "http://site-b.com/p"))

	// IP 별로 limiter 가 생성되며 각 host 의 RPH lookup 1회씩.
	assert.Equal(t, 2, configResolver.callCount(), "IP 별 limiter 첫 생성 시 host 별 lookup")
}

func TestIPRateLimiterRegistryWithResolver_ZeroRPH_NoopBypass(t *testing.T) {
	resolver := &staticIPResolver{ip: "9.9.9.9"}
	configResolver := &stubSourceConfigResolver{
		cfgs: map[string]ratelimiter.SourceConfig{
			"unlimited.example.com": {RequestsPerHour: 0}, // 제한 없음
		},
	}
	registry := ratelimiter.NewIPRateLimiterRegistryWithResolver(resolver, configResolver, 1)

	// 0 RPH → NewRateLimiter 가 noop 반환 → 무한 호출 가능.
	for i := 0; i < 50; i++ {
		require.NoError(t, registry.Wait(context.Background(), "http://unlimited.example.com/p"))
	}
}
