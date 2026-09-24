package runtimecfg

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"

	"issuetracker/pkg/config/internal/parse"
)

// PublisherCacheConfig 는 publisher 의 중복 publish 검사 캐시 설정입니다 (이슈 #507).
//
// category 페이지가 10~30분 주기로 재수집되며 거의 같은 article 링크 목록을 내놓아,
// publisher 가 링크마다 Redis SETNX 를 시도하지만 대부분 이미 잡혀 있어 버려집니다
// (라이브 51분에 10,940건). 직전에 "이미 잡힘" 으로 확인된 URL 을 기억해 왕복을 생략합니다.
type PublisherCacheConfig struct {
	// SeenCacheTTL: 기억 수명. 0 이하면 캐시 비활성 (기존 동작 — 매 URL Redis 왕복).
	// 환경변수 PUBLISHER_SEEN_CACHE_TTL (default 10m).
	//
	// **Article marker TTL (기본 24h) 보다 충분히 짧아야 합니다.** 길면 marker 가 만료된
	// URL 을 캐시가 계속 막습니다.
	SeenCacheTTL time.Duration

	// SeenCacheSize: 기억할 URL 수 상한. 0 이하면 기본 10000.
	// 환경변수 PUBLISHER_SEEN_CACHE_SIZE (default 10000).
	SeenCacheSize int
}

// DefaultPublisherCacheConfig 는 기본 설정을 반환합니다.
func DefaultPublisherCacheConfig() PublisherCacheConfig {
	return PublisherCacheConfig{
		SeenCacheTTL:  10 * time.Minute,
		SeenCacheSize: 10000,
	}
}

// LoadPublisherCache 는 .env 로드 후 환경변수로 PublisherCacheConfig 를 구성합니다.
func LoadPublisherCache(envFiles ...string) (PublisherCacheConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return PublisherCacheConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultPublisherCacheConfig()

	for _, op := range []error{
		parse.NonNegativeDuration("PUBLISHER_SEEN_CACHE_TTL", &cfg.SeenCacheTTL),
		parse.NonNegativeInt("PUBLISHER_SEEN_CACHE_SIZE", &cfg.SeenCacheSize),
	} {
		if op != nil {
			return PublisherCacheConfig{}, op
		}
	}

	return cfg, nil
}
