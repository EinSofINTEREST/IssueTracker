# pkg/agent/claude — claudegen Container Pool + LLM Session Runner

소스: [`pkg/agent/claude/`](../../../../pkg/agent/claude/)

claude Code CLI 를 docker container 안에서 호출하여 LLM 응답을 받는 agent. parser llmgen (selector 자동 생성) + enrich pipeline (4 stage) 의 공용 백엔드. 이슈 #458 (PR #459) 으로 기존 `internal/claudegen` 에서 `pkg/agent/claude` namespace 로 이전.

이슈 #474 (PR #475) 머지 후 컨테이너 default user 가 `node` (uid 1000) 로 전환되어 claude CLI 의 `--dangerously-skip-permissions` 가 root/sudo 환경에서 거부되는 회귀 해소.

<br>

## 패키지 구성

| 파일 | 역할 |
|---|---|
| [`pool.go`](../../../../pkg/agent/claude/pool.go) | `Pool` — 다중 `Worker` 를 관리, fan-out + graceful shutdown. `NewPoolFromEnv` (운영 wiring) |
| [`worker.go`](../../../../pkg/agent/claude/worker.go) | `Worker` — 단일 claudegen container 의 lifecycle + `ExtractEnriched` (parser llmgen 경로) |
| [`container.go`](../../../../pkg/agent/claude/container.go) | `execContainerRunner` — `docker run` / `docker exec` / `docker rm` CLI 래퍼 (`ContainerRunner` interface 구현) |
| [`enrich.go`](../../../../pkg/agent/claude/enrich.go) | `RunSession` — enrich 4 단계 공용 호출 (각 stage 별 file_count + prompt). 이슈 #460 으로 worker.go 에서 분리 |
| [`prompt.go`](../../../../pkg/agent/claude/prompt.go) | `buildPrompt` — file → embed loader 로 prompt 텍스트 조립 + placeholder 치환 |

<br>

## 핵심 컨셉

### 1. warm container 패턴

각 `Worker` 는 부팅 시 `docker run -d --rm` 으로 **장기 실행 claudegen container** 를 띄움. 컨테이너는 `tail -f /dev/null` 로 대기. 매 LLM 호출마다 `docker exec <container_id> claude ...` 로 새 세션을 만들지만 컨테이너 자체는 재사용 — `docker run` 오버헤드 회피.

`docker rm -f` 는 graceful shutdown 시 한 번만 (이슈 #458/#460).

### 2. ContainerRunner interface

```go
type ContainerRunner interface {
    StartContainer(ctx, image, workDir, authDir, containerAuthPath string) (containerID string, err error)
    ExecSession(ctx, containerID string, args []string) (stdout, stderr string, err error)
    StopContainer(ctx, containerID string) error
}
```

운영은 `execContainerRunner` (docker CLI). 단위 테스트는 `mockContainerRunner` 가 만족.

### 3. SessionRunner (Agent 추상)

`pkg/agent/Agent` interface — enrich/core 가 본 interface 만 의존 (의존성 역전, 이슈 #460):

```go
// internal/processor/enrich/core/claude.go
type SessionRunner = agent.Agent
```

`Worker` 가 `RunSession(ctx, label, files, promptText) (stdout, err)` 메소드로 본 interface 를 만족. enrich 단계별 (`extract` / `verify` / `context` / `score`) 가 label 로 구분.

<br>

## Container 정책 — non-root user (이슈 #474)

[deployments/docker/claudegen/Dockerfile](../../../../deployments/docker/claudegen/Dockerfile):

```dockerfile
FROM node:20-slim

# (npm install claude-code + mcp postgres + 디렉토리 준비)
RUN mkdir -p /home/node/.claude && chown -R node:node /home/node/.claude
RUN mkdir -p /workspace && chown -R node:node /workspace
WORKDIR /workspace

# non-root 로 전환 — claude CLI 의 --dangerously-skip-permissions 거부 회피
USER node

ENTRYPOINT []
CMD ["tail", "-f", "/dev/null"]
```

- node:20-slim 의 기존 `node` user (uid=1000, gid=1000) 활용 — 호스트 `juhy0987` (uid 1000) 과 일치
- 호스트 `~/.claude/` (OAuth 인증) + `~/.claude.json` (CLI 설정) mount RW 가능
- claude CLI 의 root/sudo 거부 차단

호스트 `~/.claude` 가 root 소유인 환경에서는 mount 권한 mismatch 발생 — 운영자가 `chown -R 1000:1000 ~/.claude` 또는 별도 마운트 디렉토리 생성.

<br>

## 환경 변수

| ENV | Default | 의미 |
|---|---|---|
| `CLAUDE_CODE_IMAGE` | `issuetracker-claudegen:local` | 컨테이너 이미지 (`make claudegen-build`) |
| `CLAUDE_CODE_MODEL` | `claude-sonnet-4-6` | LLM 모델 |
| `CLAUDE_CODE_TIMEOUT` | `120s` | **세션 단위 timeout** — `docker exec claude ...` 1회당 |
| `CLAUDE_CODE_WORKER_COUNT` | (default 2) | Pool 내 Worker 수 = 동시 컨테이너 수 |
| `CLAUDE_CODE_AUTH_DIR` | `$HOME/.claude` | 호스트의 claude OAuth 디렉토리 (RW mount) |
| `CLAUDE_CODE_CONTAINER_AUTH_PATH` | `/home/node/.claude` | 컨테이너 내 mount 대상 경로 (이슈 #474) |
| `CLAUDE_CODE_SHUTDOWN_TIMEOUT` | `10s` | `docker rm -f` cleanup timeout (이슈 #458) |
| `ENRICHER_DB_RO_*` | (없음) | MCP postgres tool — read-only DB 액세스 (이슈 #472) |

`CLAUDE_CODE_TIMEOUT` 의 영향 범위 + raise 시 주의사항 (특히 [`inflight_locker.DefaultInflightLockTTL`](../../../../internal/storage/redis/inflight_locker.go) 동기화) 은 이슈 #495 참조.

<br>

## 사용 경로

### 1. parser llmgen (셀렉터 자동 생성)

[`rule/llmgen.Generator`](../../internal/processor/parser/rule.md#4-llm-selector-generator) 가 `ParsePage` 실패 시 host 별로 본 Pool 의 `Worker.ExtractEnriched(host, targetType, html)` 호출. 결과 `ExtractResult` 의 `Selectors` 가 `parser_rules` 에 `enabled=false` 로 INSERT.

응답 JSON schema (`worker.go:enrichedOutput`):
```json
{
  "validity": "ok" | "blacklist",
  "blacklist_reason": "<korean>",
  "blacklist_mode": "drop" | "extract_links_only",  // 이슈 #480
  "page_type": "news" | "community" | ...,
  "page_type_confidence": <float>,
  "article": <bool>,
  "article_confidence": <float>,
  "selectors": { ... },
  "self_check": { ... }
}
```

`blacklist_mode` 가 `extract_links_only` 면 [`service.BlacklistService.HandleLLMDecision`](../../internal/storage/service.md) 가 `parser_blacklist` 에 mode 그대로 등록 (이슈 #480).

### 2. enrich 4-stage (entity/claim/verify/context/score)

[`enrich/core`](../../internal/processor/enrich/README.md) 의 4 단계 각각 `RunSession(ctx, label, files, promptText)` 호출:
- label: `enrich-extract` / `enrich-verify` / `enrich-context` / `enrich-score`
- files: stage 별 page HTML / 직전 단계 결과 JSON
- 응답: 각 단계 schema 의 JSON

응답 unmarshal 실패 (LLM 이 prose 응답 emit) 시 enrich worker 가 `forwarding without facts/verifications/context/trust_score` 로 graceful fallback (이슈 #486 / PR #487 의 프롬프트 contract 강화로 빈도 감소).

<br>

## 의존

- [`pkg/agent`](../../../../pkg/agent/) — `Agent` interface (SessionRunner 추상)
- [`pkg/llm/prompt`](../../../../pkg/llm/prompt/) — prompt loader
- [`pkg/logger`](../logger.md)
- 외부: `docker` CLI (PATH 에 있어야 함), `node:20-slim` base image

<br>

## 호출 측

| 호출자 | 사용 메소드 | 비고 |
|---|---|---|
| [`internal/processor/parser/rule/llmgen.Generator`](../../internal/processor/parser/rule.md#4-llm-selector-generator) | `Worker.ExtractEnriched(host, targetType, html)` | selector 자동 생성 |
| [`internal/processor/enrich/core.Claudegen*`](../../internal/processor/enrich/README.md) | `agent.Agent.RunSession(label, files, prompt)` (SessionRunner 추상) | 4-stage enrich |
| [`cmd/issuetracker`](../../cmd/issuetracker.md) | `NewPoolFromEnv` | wiring |

<br>

## graceful shutdown

`Pool.Stop(ctx)` 가 모든 in-flight `RunSession` / `ExtractEnriched` 완료 대기 → 각 `Worker` 의 `docker rm -f <containerID>` 호출 (CLAUDE_CODE_SHUTDOWN_TIMEOUT 10s 내). 미정상 종료 시 `--rm` 플래그로 docker 가 잔존 컨테이너 회수 (이슈 #458 의 별도 cleanup 정책 명시).

<br>

## 관련 이슈

- 이슈 #266 — claudegen 도입 (구독 quota 활용)
- 이슈 #269 — 자체 Docker 이미지 빌드 (`make claudegen-build`)
- 이슈 #270 — claudegen 버전 고정 + smoke check
- 이슈 #421 / #423 — parser_rules.article 플래그 wiring (LLM 분류)
- 이슈 #446 ~ #450 — enrich subsystem
- **이슈 #458 — claudegen → `pkg/agent/claude` namespace 분리**
- **이슈 #460 — parser/core + enrich/core — stage 별 agent 사용 OOP 분리** (`SessionRunner = agent.Agent`)
- **이슈 #470 — `--dangerously-skip-permissions` + tool 권한 자동 허가**
- **이슈 #472 — MCP postgres read-only DB 직접 접근**
- **이슈 #474 — 컨테이너 user non-root (node)**
- 이슈 #480 — `blacklist_mode` 필드 (`extract_links_only` / `drop` 분기)
- 이슈 #486 — enrich 프롬프트 JSON-only CRITICAL OUTPUT CONTRACT
- **이슈 #495 — `CLAUDE_CODE_TIMEOUT` 변경 시 `inflight_locker.DefaultInflightLockTTL` 동기화 필요** (백로그)
