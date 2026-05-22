// Stage 별 agent pool 분리를 위한 공용 인프라 (이슈 #530).
//
// 어떤 agent backend (claude / codex / gemini 등) 든 동일 패턴으로 stage 별 풀을 운용할 수
// 있도록 PoolConfig + StageEnv 를 본 패키지에 둡니다.
//
// 사용 흐름 (각 agent provider 가 자체 NewPoolFromConfig 구현 시):
//
//  1. main.go 가 PoolConfig{Name: "<stage>"} 로 호출
//  2. agent provider 가 PoolConfig.Name 으로 StageEnv 생성 → env prefix 적용
//  3. agent 별 base env (CLAUDE_CODE_*, GEMINI_*, OPENAI_*) 을 stage prefix 우선 lookup
//
// 따라서 같은 PoolConfig API 로 모든 agent 가 stage 별 분리됩니다.
package agent

import (
	"os"
	"strings"
)

// PoolConfig 는 모든 agent provider 가 받는 공용 풀 옵션입니다.
//
// agent 별 추가 옵션이 필요하면 provider 패키지가 embed 또는 type alias 로 확장:
//
//	type PoolConfig struct {
//	    agent.PoolConfig
//	    // 추가 필드...
//	}
//
// Name 만으로 충분한 일반 case 는 본 struct 를 직접 사용.
type PoolConfig struct {
	// Name 은 stage 식별자 — env prefix + 로깅 필드.
	// 빈 문자열은 backward-compat (prefix 없이 base env 만 lookup).
	// 정규화: 공백 / 하이픈 / 점 → underscore. UPPER 변환.
	// 예: "parser" → "PARSER_", "parser-llm" → "PARSER_LLM_".
	Name string
}

// stageNameReplacer 는 stage 이름의 비-식별자 문자를 underscore 로 정규화합니다.
var stageNameReplacer = strings.NewReplacer(" ", "_", "-", "_", ".", "_")

// StageEnv 는 stage prefix 우선 + base fallback 의 환경변수 해석기입니다 (이슈 #530).
//
// Name 이 빈 문자열이면 stage prefix 없이 base 만 lookup — backward-compat.
//
// 사용 예 (claude 가 stage="parser" 로 호출):
//
//	e := agent.NewStageEnv("parser")
//	count := e.GetOr("CLAUDE_CODE_WORKER_COUNT", "2")
//	// PARSER_CLAUDE_CODE_WORKER_COUNT 우선, 없으면 CLAUDE_CODE_WORKER_COUNT, 없으면 "2"
type StageEnv struct {
	name string
}

// NewStageEnv 는 stage 이름으로 StageEnv 를 생성합니다.
// 빈 문자열은 backward-compat (base env 만 lookup).
func NewStageEnv(name string) StageEnv {
	return StageEnv{name: name}
}

// Name 은 stage 이름을 반환합니다 (로깅용).
func (s StageEnv) Name() string { return s.name }

// Prefix 는 stage 이름을 UPPER_SNAKE 로 정규화한 env prefix 를 반환합니다 (끝에 "_" 부착).
// 빈 이름이면 "".
func (s StageEnv) Prefix() string {
	if s.name == "" {
		return ""
	}
	return strings.ToUpper(stageNameReplacer.Replace(s.name)) + "_"
}

// Get 은 stage prefix 우선 + base fallback 으로 환경변수 값을 반환합니다.
//
// 둘 다 미설정이면 빈 문자열. 빈 값은 미설정과 동일하게 취급 — 다음 단계로 fallback.
// 반환된 (value, key) 에서 key 는 실제 lookup 에 사용된 변수명 (로깅용).
func (s StageEnv) Get(base string) (value, key string) {
	if p := s.Prefix(); p != "" {
		k := p + base
		if v := os.Getenv(k); v != "" {
			return v, k
		}
	}
	if v := os.Getenv(base); v != "" {
		return v, base
	}
	return "", base
}

// GetOr 는 Get 결과가 빈 값이면 def 를 반환합니다 — 단순 lookup 용.
func (s StageEnv) GetOr(base, def string) string {
	v, _ := s.Get(base)
	if v == "" {
		return def
	}
	return v
}
