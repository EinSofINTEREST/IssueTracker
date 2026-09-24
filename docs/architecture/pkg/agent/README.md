# pkg/agent — LLM Agent Adapters

소스: [`pkg/agent/`](../../../../pkg/agent/)

LLM 응답을 도구 호출 권한 + 컨테이너 격리 환경에서 받기 위한 agent adapter 들의 namespace. 이슈 #458 (PR #459) 에서 `internal/claudegen` 을 본 namespace 로 이전.

<br>

## Sub-package

| Sub-package | 문서 | 책임 |
|---|---|---|
| [`pkg/agent/claude/`](../../../../pkg/agent/claude/) | [claude.md](claude.md) | claude Code CLI + docker container 기반 agent. parser llmgen + enrich subsystem 의 공용 백엔드 |
| [`pkg/agent/codex/`](../../../../pkg/agent/codex/) | [codex.md](codex.md) | Codex CLI + docker container 기반 agent. claude 와 동일 인터페이스의 두 번째 backend (메타 이슈 #462). **기본 비활성** |
| [`pkg/agent/dependency/db/`](../../../../pkg/agent/dependency/db/) | (별도 문서 없음) | MCP postgres tool 설정 builder — enricher_ro role 의 read-only DB 액세스 (이슈 #472). **claude 전용** — codex 는 MCP 미지원 (이슈 #585) |

<br>

## Agent interface

각 sub-package 가 만족하는 공용 interface:

```go
// pkg/agent/agent.go
type Agent interface {
    RunSession(
        ctx context.Context,
        sessionLabel string,
        files map[string][]byte,
        prompt string,
    ) (stdout string, err error)
}
```

`internal/processor/enrich/core` 는 `SessionRunner = agent.Agent` alias 로 참조한다.

호출자 (`enrich/core` 의 4 단계 등) 는 본 interface 만 의존 — 백엔드 교체 가능. 의존성 역전 (이슈 #460).

<br>

## Backend 선택 정책 (이슈 #534)

stage 별로 어느 backend 의 풀을 쓸지 환경변수로 고른다.

| ENV | 값 | Default |
|---|---|---|
| `PARSER_AGENT_BACKEND` | `claude` \| `codex` | `claude` |
| `ENRICH_AGENT_BACKEND` | `claude` \| `codex` | `claude` |

해석은 [`agent.NormalizeBackend`](../../../../pkg/agent/backend.go) 가 담당한다 —
대소문자 / 앞뒤 공백 무시, 인식 불가한 값은 기본값(claude) + WARN. 오타 하나로 프로세스
기동이 막히는 것보다 안전하기 때문이다.

### 선택한 backend 의 풀이 없으면 — 대체하지 않는다

가장 중요한 규칙이다. 예를 들어 `PARSER_AGENT_BACKEND=codex` 인데
`CODEX_AGENT_ENABLED=false` 면, parser 는 **claude 로 대체되지 않고** agent 경로 없이
(기본 LLM provider 로) 동작한다.

명시한 backend 를 조용히 바꾸면 모델과 비용이 의도와 달라지고, 로그를 뒤지기 전까지
알아챌 수 없다. 이 경우 INFO 로그가 남는다:

```
selected agent backend has no running pool; agent path disabled for this stage
```

### 풀 생성 조건은 선택과 별개다

| backend | 풀 생성 조건 |
|---|---|
| claude | `LLM_EXTRACTOR=claude-code` + prompt loader / LLM generator 활성 |
| codex | `CODEX_AGENT_ENABLED=true` + prompt loader 활성 |

여기에 stage 가드 (`STAGES_PARSER_ENABLED` / `STAGES_ENRICH_ENABLED`) 가 추가로 걸린다 —
비활성 stage 의 idle 컨테이너 비용을 피하기 위함이다.

두 backend 를 동시에 켜면 최대 **4 풀** (backend 2 × stage 2) 이 상주한다. 비교 운영에는
유용하나 컨테이너 수가 배가 되므로, 비교가 끝나면 쓰지 않는 쪽의 생성 조건을 끌 것.
**선택에서 탈락한 풀도 컨테이너는 떠 있으며**, 종료 시 함께 정리된다 (로그의
`agent_backend` 필드로 구별).

<br>

## 관련 이슈

- 이슈 #458 — `internal/claudegen` → `pkg/agent/claude` 이전
- 이슈 #460 — stage 별 agent 사용 OOP 분리 (Agent interface)
- 이슈 #472 — MCP postgres tool (`pkg/agent/dependency/db`)
- 이슈 #474 — claude 컨테이너 user non-root
- **이슈 #462 — codex backend 도입 (메타)** — #532 골격 / #533 이미지 / #534 wiring / #535 테스트
- **이슈 #534 — stage 별 agent backend 선택 정책**
- 이슈 #537 — 생성자의 authDir 즉시 검증 제거 (두 backend 공통)
- 이슈 #585 — codex MCP 지원 (현재 미지원)
