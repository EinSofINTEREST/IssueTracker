package llmcfg

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// PromptConfig 는 LLM prompt loader 의 wiring 설정입니다.
//
// pkg/llm/prompt 패키지의 NewDefaultLoader 가 본 설정으로 외부 파일 / embed fallback 정책을
// 결정. 도메인 패키지는 본 config 타입을 import 하지 않음 — cmd/* 가 LoadPrompt 결과의
// primitives (Dir, DirSet) 를 도메인 함수에 전달.
type PromptConfig struct {
	// Dir 은 LLM_PROMPT_DIR 환경변수 값 (DirSet=true 일 때만 의미 있음).
	// 빈 문자열 + DirSet=true → embed-only 강제 운영 의도.
	Dir string

	// DirSet 은 LLM_PROMPT_DIR 환경변수가 설정되었는지 여부 (값이 빈 문자열이라도 true).
	// os.Getenv 로는 unset 과 빈값 구분 불가하므로 명시 분리.
	DirSet bool
}

// LoadPrompt 는 .env 파일을 로드한 후 LLM_PROMPT_DIR 환경변수로 PromptConfig 를 구성합니다.
//
// 의미:
//   - 미설정      → DirSet=false   (cmd 가 DefaultDir auto-detection 시도)
//   - 빈 값 명시  → DirSet=true, Dir="" (embed-only 강제)
//   - 값 명시     → DirSet=true, Dir=값 (file → embed chain)
func LoadPrompt(envFiles ...string) (PromptConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return PromptConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	v, ok := os.LookupEnv("LLM_PROMPT_DIR")
	return PromptConfig{Dir: v, DirSet: ok}, nil
}
