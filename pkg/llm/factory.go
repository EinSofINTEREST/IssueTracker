package llm

import (
	"fmt"
	"strings"
	"time"
)

// Config 는 Provider 생성을 위한 설정입니다.
//
// Config drives Provider construction in a backend-agnostic way.
// Provider 교체는 본 Config 의 Provider 필드 한 줄 변경으로 가능합니다.
type Config struct {
	// Provider 식별자: "gemini" / "openai" / "anthropic" (대소문자 무시).
	Provider string

	// APIKey — provider 별 API key. 비어있으면 호출 시 ErrCodeAuth.
	APIKey string

	// Model — 기본 모델 (선택). 비어있으면 provider 별 default 사용.
	Model string

	// BaseURL — REST endpoint root (선택, 보통 테스트의 mock server 주입용).
	BaseURL string

	// Timeout — HTTP timeout (선택, default 60s).
	Timeout time.Duration
}

// providerFactories 는 등록된 provider 빌더 함수 맵입니다.
// 신규 provider 추가는 init() 에서 RegisterProvider 호출로 확장 가능 (현재는 패키지 init 미사용,
// llm/<provider>/<provider>.go 모두 본 패키지가 직접 의존하지 않도록 builder 함수만 등록).
//
// 본 맵은 New() 에서만 read 되므로 lock 불필요 — init 시점에만 채워짐.
var providerFactories = map[string]func(Config) (Provider, error){}

// RegisterProvider 는 외부 패키지가 자기 builder 를 등록할 수 있게 합니다.
// 일반적으로 각 provider 패키지의 init() 에서 호출됩니다.
//
// 본 함수는 thread-safe 하지 않으며, 패키지 init 시점에만 호출되어야 합니다.
func RegisterProvider(name string, builder func(Config) (Provider, error)) {
	providerFactories[strings.ToLower(name)] = builder
}

// New 는 Config 에 따라 적절한 Provider 를 생성합니다.
//
// Provider 식별자는 대소문자 무시 ("Gemini" / "GEMINI" / "gemini" 모두 동일).
// 미등록 provider 는 ErrCodeBadRequest 반환 (호출자가 등록 누락 인지 가능).
func New(cfg Config) (Provider, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if name == "" {
		return nil, &Error{
			Code:     ErrCodeBadRequest,
			Provider: "factory",
			Message:  "Config.Provider is empty",
		}
	}
	builder, ok := providerFactories[name]
	if !ok {
		return nil, &Error{
			Code:     ErrCodeBadRequest,
			Provider: "factory",
			Message:  fmt.Sprintf("unknown provider %q (registered: %v)", cfg.Provider, registeredNames()),
		}
	}
	return builder(cfg)
}

// registeredNames 는 디버깅용 — 현재 등록된 provider 이름 목록을 반환합니다.
func registeredNames() []string {
	names := make([]string, 0, len(providerFactories))
	for n := range providerFactories {
		names = append(names, n)
	}
	return names
}
