// Package processorcfg 는 .env 파일과 환경변수를 통해 processor 도메인 설정을 관리합니다.
// godotenv.Load() 후 OS 환경변수가 우선 적용됩니다.
package processorcfg

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// BlacklistConfig 는 page-parse 블랙리스트 wiring 설정입니다.
//
// Enabled=false 면 BlacklistMatcher 미주입 — parser_worker.processCategoryPage 가 모든 카테고리
// 링크를 그대로 article job 으로 발행 (기능 OFF). 운영 toggle 용도.
type BlacklistConfig struct {
	// Enabled: 환경변수 BLACKLIST_ENABLED (default true).
	Enabled bool
}

// DefaultBlacklistConfig 는 기본 BlacklistConfig 를 반환합니다.
func DefaultBlacklistConfig() BlacklistConfig {
	return BlacklistConfig{Enabled: true}
}

// LoadBlacklist 는 .env 를 로드한 후 OS 환경변수로 BlacklistConfig 를 구성합니다.
//
// 지원 환경변수:
//   - BLACKLIST_ENABLED: true | false (default true)
func LoadBlacklist(envFiles ...string) (BlacklistConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return BlacklistConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultBlacklistConfig()
	if v := os.Getenv("BLACKLIST_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return BlacklistConfig{}, fmt.Errorf("parse BLACKLIST_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	return cfg, nil
}
