package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// MetricsConfig는 Prometheus /metrics endpoint 노출 설정을 나타냅니다.
//
// MetricsConfig holds settings for the Prometheus /metrics HTTP endpoint.
type MetricsConfig struct {
	// Addr: /metrics 노출 listen 주소 (예: ":9090"). 빈 문자열이면 endpoint 비활성화.
	Addr string
}

// DefaultMetricsConfig는 기본 MetricsConfig를 반환합니다 (default Addr ":9090").
func DefaultMetricsConfig() MetricsConfig {
	return MetricsConfig{Addr: ":9090"}
}

// LoadMetrics는 .env 파일을 로드한 후 OS 환경변수로 MetricsConfig를 구성합니다.
// 지원 환경변수: METRICS_ADDR (예: ":9090", 빈 값이면 endpoint 비활성화).
func LoadMetrics(envFiles ...string) (MetricsConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return MetricsConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultMetricsConfig()
	// METRICS_ADDR 가 set 되지 않은 경우 default ":9090" 유지.
	// 빈 문자열로 명시 set ("METRICS_ADDR=") 하면 endpoint 비활성화.
	if v, ok := os.LookupEnv("METRICS_ADDR"); ok {
		cfg.Addr = v
	}
	return cfg, nil
}
