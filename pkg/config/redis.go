package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// RedisConfig는 Redis 연결 설정을 나타냅니다.
// 모든 필드는 환경변수(LoadRedis 참조)로 덮어쓸 수 있습니다.
type RedisConfig struct {
	Host         string        // REDIS_HOST (default: localhost)
	Port         int           // REDIS_PORT (default: 6379)
	Password     string        // REDIS_PASSWORD (default: "")
	DB           int           // REDIS_DB (default: 0)
	DialTimeout  time.Duration // REDIS_DIAL_TIMEOUT (default: 5s)
	ReadTimeout  time.Duration // REDIS_READ_TIMEOUT (default: 3s)
	WriteTimeout time.Duration // REDIS_WRITE_TIMEOUT (default: 3s)
	PoolSize     int           // REDIS_POOL_SIZE (default: 10)
	// IngestionLockTTL: 파이프라인 진입 marker 의 TTL.
	// publisher 가 atomic SETNX 로 marker 를 잡고, 본 TTL 만료 시 자연스럽게 재크롤 가능.
	// 환경변수: REDIS_INGESTION_LOCK_TTL (default 24h).
	IngestionLockTTL time.Duration

	// PipelineGuardCategoryTTL: PipelineGuard 의 Category target 전용 단명 TTL.
	// fetch + ParseLinks 한 cycle 진행 중에만 marker 유지 — 정상 흐름은 명시적 Release,
	// 본 TTL 은 fallback (worker 가 release 호출 못 하고 죽은 경우 자동 회수).
	// 환경변수: PIPELINE_GUARD_CATEGORY_TTL (default 60s).
	PipelineGuardCategoryTTL time.Duration
}

// DefaultRedisConfig는 로컬 개발 환경용 기본 RedisConfig를 반환합니다.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		Host:                     "localhost",
		Port:                     6379,
		Password:                 "",
		DB:                       0,
		DialTimeout:              5 * time.Second,
		ReadTimeout:              3 * time.Second,
		WriteTimeout:             3 * time.Second,
		PoolSize:                 10,
		IngestionLockTTL:         24 * time.Hour,
		PipelineGuardCategoryTTL: 60 * time.Second,
	}
}

// LoadRedis는 .env 파일을 로드한 후 OS 환경변수로 RedisConfig를 구성합니다.
// 환경변수 값이 설정되어 있지만 파싱에 실패하거나 범위를 벗어나면 에러를 반환합니다.
func LoadRedis(envFiles ...string) (RedisConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RedisConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultRedisConfig()

	if v := os.Getenv("REDIS_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("REDIS_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_PORT %q: %w", v, err)
		}
		if p < 1 || p > 65535 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_PORT %d: must be between 1 and 65535", p)
		}
		cfg.Port = p
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Password = v
	}
	if v := os.Getenv("REDIS_DB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_DB %q: %w", v, err)
		}
		if n < 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_DB %d: must be 0 or greater", n)
		}
		cfg.DB = n
	}
	if v := os.Getenv("REDIS_DIAL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_DIAL_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_DIAL_TIMEOUT %q: must be positive", v)
		}
		cfg.DialTimeout = d
	}
	if v := os.Getenv("REDIS_READ_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_READ_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_READ_TIMEOUT %q: must be positive", v)
		}
		cfg.ReadTimeout = d
	}
	if v := os.Getenv("REDIS_WRITE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_WRITE_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_WRITE_TIMEOUT %q: must be positive", v)
		}
		cfg.WriteTimeout = d
	}
	if v := os.Getenv("REDIS_POOL_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_POOL_SIZE %q: %w", v, err)
		}
		if n < 1 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_POOL_SIZE %d: must be 1 or greater", n)
		}
		cfg.PoolSize = n
	}
	if v := os.Getenv("REDIS_INGESTION_LOCK_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_INGESTION_LOCK_TTL %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_INGESTION_LOCK_TTL %q: must be positive", v)
		}
		cfg.IngestionLockTTL = d
	}
	if v := os.Getenv("PIPELINE_GUARD_CATEGORY_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse PIPELINE_GUARD_CATEGORY_TTL %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid PIPELINE_GUARD_CATEGORY_TTL %q: must be positive", v)
		}
		cfg.PipelineGuardCategoryTTL = d
	}

	return cfg, nil
}
