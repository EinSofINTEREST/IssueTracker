package core

import (
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"
)

// IPResolver는 URL에서 목적지 IP를 해석하는 인터페이스입니다.
// 구현체는 goroutine-safe해야 합니다.
//
// IPResolver resolves the destination IP address for a given URL.
type IPResolver interface {
	// Resolve는 URL의 호스트에 대해 DNS 해석을 수행하고 첫 번째 IP를 반환합니다.
	Resolve(rawURL string) (string, error)
}

// DNSIPResolver는 net.LookupHost 기반 IPResolver 구현체입니다.
// DNS 결과를 TTL 동안 캐시하여 반복 해석을 방지합니다.
//
// DNSIPResolver resolves IPs via net.LookupHost with an in-memory TTL cache.
type DNSIPResolver struct {
	cache map[string]dnsEntry
	mu    sync.RWMutex
	ttl   time.Duration
}

type dnsEntry struct {
	ip        string
	expiresAt time.Time
}

// NewDNSIPResolver는 지정된 캐시 TTL로 DNSIPResolver를 생성합니다.
func NewDNSIPResolver(cacheTTL time.Duration) *DNSIPResolver {
	return &DNSIPResolver{
		cache: make(map[string]dnsEntry),
		ttl:   cacheTTL,
	}
}

// Resolve는 URL의 호스트를 DNS로 해석하여 첫 번째 IP를 반환합니다.
// 캐시된 결과가 유효하면 DNS 조회 없이 반환합니다.
func (r *DNSIPResolver) Resolve(rawURL string) (string, error) {
	host, err := extractHost(rawURL)
	if err != nil {
		return "", err
	}

	// 캐시 확인 (read lock)
	r.mu.RLock()
	if entry, ok := r.cache[host]; ok && time.Now().Before(entry.expiresAt) {
		r.mu.RUnlock()
		return entry.ip, nil
	}
	r.mu.RUnlock()

	// DNS 해석
	ips, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("dns lookup %s: %w", host, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("dns lookup %s: no addresses found", host)
	}

	ip := ips[0]

	// 캐시 저장 (write lock)
	r.mu.Lock()
	r.cache[host] = dnsEntry{ip: ip, expiresAt: time.Now().Add(r.ttl)}
	r.mu.Unlock()

	return ip, nil
}

// extractHost는 URL에서 호스트(포트 제외)를 추출합니다.
func extractHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("empty host in url %q", rawURL)
	}
	return host, nil
}
