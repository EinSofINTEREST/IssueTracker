package rate_limiter_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ratelimiter "issuetracker/internal/crawler/rate_limiter"
)

func TestDNSIPResolver_Resolve_Localhost(t *testing.T) {
	resolver := ratelimiter.NewDNSIPResolver(5 * time.Minute)

	ip, err := resolver.Resolve(context.Background(), "http://localhost:8080/path")
	require.NoError(t, err)
	assert.NotEmpty(t, ip)

	// net.ParseIP + IsLoopback로 환경 독립적 검증
	parsed := net.ParseIP(ip)
	require.NotNil(t, parsed, "resolved IP %q should be parseable", ip)
	assert.True(t, parsed.IsLoopback(), "resolved IP %q should be loopback", ip)
}

func TestDNSIPResolver_Resolve_CachesResult(t *testing.T) {
	resolver := ratelimiter.NewDNSIPResolver(1 * time.Hour)

	ip1, err := resolver.Resolve(context.Background(), "http://localhost/a")
	require.NoError(t, err)

	ip2, err := resolver.Resolve(context.Background(), "http://localhost/b")
	require.NoError(t, err)

	// 동일 호스트는 캐시된 동일 IP를 반환
	assert.Equal(t, ip1, ip2)
}

func TestDNSIPResolver_Resolve_InvalidURL_ReturnsError(t *testing.T) {
	resolver := ratelimiter.NewDNSIPResolver(5 * time.Minute)

	_, err := resolver.Resolve(context.Background(), "://invalid")
	assert.Error(t, err)
}

func TestDNSIPResolver_Resolve_EmptyHost_ReturnsError(t *testing.T) {
	resolver := ratelimiter.NewDNSIPResolver(5 * time.Minute)

	_, err := resolver.Resolve(context.Background(), "http:///path-only")
	assert.Error(t, err)
}

func TestDNSIPResolver_Resolve_UnresolvableHost_ReturnsError(t *testing.T) {
	resolver := ratelimiter.NewDNSIPResolver(5 * time.Minute)

	_, err := resolver.Resolve(context.Background(), "http://this-host-does-not-exist-12345.invalid/path")
	assert.Error(t, err)
}

func TestDNSIPResolver_Resolve_ContextCanceled(t *testing.T) {
	resolver := ratelimiter.NewDNSIPResolver(5 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolver.Resolve(ctx, "http://this-host-does-not-exist-12345.invalid/path")
	assert.Error(t, err)
}
