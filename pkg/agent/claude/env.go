// Stage prefix 인지 환경변수 헬퍼 (이슈 #530).
//
// parser / enrich 등 stage 마다 독립 claude.Pool 을 구성할 수 있도록 env 해석 단계에서
// stage prefix 를 적용합니다. 우선순위:
//
//  1. <STAGE>_<BASE>  — stage 별 명시 (예: PARSER_CLAUDE_CODE_WORKER_COUNT)
//  2. <BASE>          — fallback (예: CLAUDE_CODE_WORKER_COUNT)
//  3. default 상수
//
// 신규 stage 가 추가될 때 stageEnv 만 새 이름으로 만들면 동일 env 명명 규칙 적용됨.
package claude

import (
	"os"
	"strings"
)

// stageEnv 는 stage prefix 우선 + base fallback 의 환경변수 해석기입니다.
//
// name 이 빈 문자열이면 stage prefix 없이 base 만 lookup — 기존 단일 풀 호환 경로.
type stageEnv struct {
	name string
}

// prefix 는 stage 이름을 UPPER_SNAKE 로 정규화하여 반환합니다. 빈 이름이면 "".
func (s stageEnv) prefix() string {
	if s.name == "" {
		return ""
	}
	return strings.ToUpper(s.name) + "_"
}

// get 은 stage prefix 우선 + base fallback 으로 환경변수 값을 반환합니다.
//
// 둘 다 미설정이면 빈 문자열. 빈 값은 미설정과 동일하게 취급 — 다음 단계로 fallback.
// 반환된 (value, key) 에서 key 는 실제 lookup 에 사용된 변수명 (로깅용).
func (s stageEnv) get(base string) (value, key string) {
	if p := s.prefix(); p != "" {
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

// getOr 는 get 결과가 빈 값이면 def 를 반환합니다 — 단순 lookup 용.
func (s stageEnv) getOr(base, def string) string {
	v, _ := s.get(base)
	if v == "" {
		return def
	}
	return v
}
