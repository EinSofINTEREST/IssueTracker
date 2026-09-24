package agent

import "strings"

// Backend 는 stage 가 사용할 agent 구현을 식별합니다 (이슈 #534).
//
// 값은 `PARSER_AGENT_BACKEND` / `ENRICH_AGENT_BACKEND` 환경변수로 지정하며, 실제 풀 선택은
// 호출자 (main wiring) 가 수행합니다 — 본 패키지는 backend 구현체를 import 하지 않습니다
// (claude / codex 패키지가 본 패키지를 import 하므로 반대 방향은 순환 의존).
type Backend string

const (
	BackendClaude Backend = "claude"
	BackendCodex  Backend = "codex"

	// DefaultBackend 는 미지정 / 인식 불가 시 사용할 backend 입니다.
	DefaultBackend = BackendClaude
)

// NormalizeBackend 는 환경변수 문자열을 Backend 로 정규화합니다.
//
// 반환값의 bool 은 **입력을 인식했는지** 를 나타냅니다:
//   - 빈 문자열 (미지정) → (DefaultBackend, true) — 설정하지 않은 것은 오류가 아닙니다
//   - "claude" / "codex" (대소문자 / 앞뒤 공백 무시) → (해당 Backend, true)
//   - 그 외 → (DefaultBackend, false) — 호출자가 WARN 을 남길 수 있도록 false
//
// 인식 불가를 에러가 아니라 기본값 + false 로 돌려주는 이유: 오타 하나로 프로세스 기동이
// 막히는 것보다, 기본 backend 로 뜨고 경고를 남기는 편이 운영상 안전합니다.
func NormalizeBackend(raw string) (Backend, bool) {
	switch b := Backend(strings.ToLower(strings.TrimSpace(raw))); b {
	case "":
		return DefaultBackend, true
	case BackendClaude, BackendCodex:
		return b, true
	default:
		return DefaultBackend, false
	}
}
