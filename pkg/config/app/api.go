package appcfg

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// APIConfig는 REST API 서버의 listen 설정을 나타냅니다 (이슈 #635).
//
// APIConfig holds listen settings for the REST API server.
type APIConfig struct {
	// Addr: API 서버 listen 주소. 빈 문자열이면 서버 비활성화.
	Addr string

	// AuthToken: Bearer 토큰. 빈 문자열이면 인증 비활성화 (기본, 이슈 #650).
	AuthToken string
}

// DefaultAPIConfig는 기본 APIConfig를 반환합니다.
//
// 기본값이 loopback 인 이유: 본 API 는 인증을 갖지 않습니다 (이슈 #635 범위 밖).
// ":8080" 을 기본으로 두면 배포 즉시 외부에 열리므로, 노출은 운영자가 API_ADDR 을
// 명시적으로 바꿀 때만 일어나도록 합니다.
func DefaultAPIConfig() APIConfig {
	return APIConfig{Addr: "127.0.0.1:8080"}
}

// LoadAPI는 .env 파일을 로드한 후 OS 환경변수로 APIConfig를 구성합니다.
// 지원 환경변수:
//   - API_ADDR       (예: "127.0.0.1:8080", 빈 값이면 서버 비활성화)
//   - API_AUTH_TOKEN (빈 값이면 인증 비활성화)
func LoadAPI(envFiles ...string) (APIConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return APIConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultAPIConfig()
	// API_ADDR 미설정 시 loopback 기본값 유지. 빈 문자열로 명시 set ("API_ADDR=") 하면 비활성화.
	if v, ok := os.LookupEnv("API_ADDR"); ok {
		cfg.Addr = v
	}
	cfg.AuthToken = os.Getenv("API_AUTH_TOKEN")
	return cfg, nil
}
