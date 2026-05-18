# pkg/agent — LLM Agent Adapters

소스: [`pkg/agent/`](../../../../pkg/agent/)

LLM 응답을 도구 호출 권한 + 컨테이너 격리 환경에서 받기 위한 agent adapter 들의 namespace. 이슈 #458 (PR #459) 에서 `internal/claudegen` 을 본 namespace 로 이전.

<br>

## Sub-package

| Sub-package | 문서 | 책임 |
|---|---|---|
| [`pkg/agent/claude/`](../../../../pkg/agent/claude/) | [claude.md](claude.md) | claude Code CLI + docker container 기반 agent. parser llmgen + enrich subsystem 의 공용 백엔드 |
| [`pkg/agent/dependency/db/`](../../../../pkg/agent/dependency/db/) | (별도 문서 없음) | MCP postgres tool 설정 builder — enricher_ro role 의 read-only DB 액세스 (이슈 #472) |

<br>

## Agent interface

각 sub-package 가 만족하는 공용 interface:

```go
// pkg/agent/Agent (또는 SessionRunner alias)
type Agent interface {
    RunSession(ctx context.Context, label string, files map[string]string, promptText string) (stdout string, err error)
}
```

호출자 (`enrich/core` 의 4 단계 등) 는 본 interface 만 의존 — 백엔드 교체 가능 (claudegen / future agents). 의존성 역전 (이슈 #460).

<br>

## 관련 이슈

- 이슈 #458 — `internal/claudegen` → `pkg/agent/claude` 이전
- 이슈 #460 — stage 별 agent 사용 OOP 분리 (Agent interface)
- 이슈 #472 — MCP postgres tool (`pkg/agent/dependency/db`)
- 이슈 #474 — claude 컨테이너 user non-root
