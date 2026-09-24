# pkg/agent/codex — Codex CLI Container Pool

소스: [`pkg/agent/codex/`](../../../../pkg/agent/codex/)

OpenAI Codex CLI 를 docker container 안에서 호출하는 agent. [claude.md](claude.md) 의
claudegen 과 **같은 인터페이스를 만족하는 두 번째 backend** 로, parser llmgen (selector 자동
생성) 과 enrich 4-stage 가 backend 교체만으로 양쪽을 쓸 수 있다.

메타 이슈 #462 로 도입 — Sub 1 골격 (#532) / Sub 2 이미지 (#533) / Sub 3 wiring (#534) /
Sub 4 테스트 (#535).

기본 비활성이다. `CODEX_AGENT_ENABLED=true` 이어야 풀이 생성된다.

<br>

## claude 와 무엇이 다른가

두 패키지는 구조가 거의 같으므로, **다른 점만** 먼저 본다. 같은 줄 알고 접근하면 틀리는
지점들이다.

| 항목 | claude | codex |
|---|---|---|
| CLI 호출 | `claude ... -p <prompt>` (플래그) | `codex exec ... <prompt>` — **프롬프트가 마지막 위치 인자** |
| git repo 검사 | 없음 | **`--skip-git-repo-check` 필수** (아래 참조) |
| parser prompt asset | `parser/claude/*` | **claude 와 공용** — 전용 asset 없음 (이슈 #594) |
| MCP (DB 도구) | `.mcp.json` 파일 mount (이슈 #472) | `-c mcp_servers.<name>` override — **호출 단위, 비영구** (아래 참조) |
| 고아 workspace 정리 | `CleanupOrphanedWorkspaces` (이슈 #539) | **없음** (아래 참조) |
| 베이스 이미지 | `node:20-slim` | `node:22-bookworm-slim` |
| 인증 디렉토리 | `~/.claude` + `~/.claude.json` | `~/.codex` |
| 빌드 | `make claudegen-build` | `make codex-build` |

그 외 — warm container 패턴, `ContainerRunner` 추상, `Pool` 의 round-robin 분배,
graceful shutdown, 응답 JSON schema — 는 claude 와 동일하다. [claude.md](claude.md) 참조.

<br>

## 패키지 구성

| 파일 | 역할 |
|---|---|
| [`pool.go`](../../../../pkg/agent/codex/pool.go) | `Pool` — Worker N replica 를 round-robin 분배. `NewPoolFromConfig` (stage 별 풀) |
| [`worker.go`](../../../../pkg/agent/codex/worker.go) | `Worker` — 단일 컨테이너 lifecycle + `ExtractEnriched` (parser llmgen 경로) |
| [`session.go`](../../../../pkg/agent/codex/session.go) | `RunSession` — enrich 4 단계 공용 호출 (generic session primitive) |
| [`container.go`](../../../../pkg/agent/codex/container.go) | `execContainerRunner` — `docker run` / `exec` / `rm` CLI 래퍼 |
| [`prompt.go`](../../../../pkg/agent/codex/prompt.go) | `buildPrompt` — prompt asset 로드 + placeholder 치환 |

<br>

## MCP 전달 방식 (이슈 #585)

codex 의 `exec` 하위명령에는 `--mcp-config` 같은 **파일 주입 옵션이 없다.** claude 의
`.mcp.json` mount 를 그대로 옮겨 쓸 수 없고, 설정을 넣는 경로는 두 가지뿐이다.

| 경로 | 지속성 | 채택 |
|---|---|---|
| `codex mcp add` → `$CODEX_HOME/config.toml` | **영구** | ❌ |
| `-c mcp_servers.<name>.<field>=<TOML>` | 호출 단위 | ✅ |

**파일 경로를 쓰지 않는 이유가 결정적이다.** `$CODEX_HOME` 은 호스트 `~/.codex` 가 RW 로
마운트된 곳이다. 컨테이너 안에서 등록하면 호스트 설정 파일에 DSN 이 평문으로 **영구
기록** 되고, 세션이 끝나도 남으며 다른 용도의 codex 사용에도 적용된다.

변환은 [`mcp.go`](../../../../pkg/agent/codex/mcp.go) 의 `mcpOverrideArgs` 가 담당한다.
값을 직접 TOML 로 인용하는데, codex 가 파싱 실패 시 **raw 문자열로 취급** 해 배열 등이
에러 없이 조용히 격하되기 때문이다. 서버 이름과 env 키는 정렬해 같은 설정이 항상 같은
인자를 만들도록 고정한다.

### 노출 면 — 알고 채택한 대가

`-c` 값은 **argv 에 실린다.** 컨테이너 내 `ps` 에서 보이므로 claude 의 파일 mount 보다
노출 면이 넓다.

- 완화: DSN 류는 `args` 가 아니라 `env` 로 넘기는 구성을 권장한다 (변환기가 `env` 하위
  키를 지원한다)
- 근본: `enricher_ro` 가 SELECT-only role (migration 031) 이라는 점이 보안 layer 다

대안인 파일 경로는 위에서 본 대로 호스트 영구 기록이라 더 나쁘다.

<br>

## 고아 workspace 정리 없음

claude `Worker.Start` 는 `writeWorkspaceOwner` 로 PID 를 기록해, SIGKILL / OOM 후 남은
workspace 를 다음 기동의 `CleanupOrphanedWorkspaces` 가 회수한다 (이슈 #539).

**codex 에는 이 경로가 없다.** claude 에서 이 장치가 필요했던 주된 이유는 workspace 에
남는 `.mcp.json` (enricher_ro 자격증명) 이었는데, codex 는 MCP 를 쓰지 않아 자격증명이
남지 않는다. 다만 `/tmp/codex-workspace-*` 디렉토리 자체는 비정상 종료 시 누적된다.

정상 종료 (`Pool.Stop` → `Worker.Stop`) 경로에서는 삭제되므로, 운영 중 프로세스가 자주
강제 종료되는 환경이 아니면 문제되지 않는다. 필요해지면 별도 이슈로 다룬다.

<br>

## Container 정책

[deployments/docker/codex/Dockerfile](../../../../deployments/docker/codex/Dockerfile):

```dockerfile
FROM node:22-bookworm-slim

ARG CODEX_VERSION=0.156.1
RUN npm install -g @openai/codex@${CODEX_VERSION} \
    && codex --version

RUN mkdir -p /home/node/.codex && chown -R node:node /home/node/.codex
RUN mkdir -p /workspace && chown -R node:node /workspace
WORKDIR /workspace

USER node

ENTRYPOINT []
CMD ["tail", "-f", "/dev/null"]
```

- 버전 고정 + 설치 직후 `codex --version` smoke check — 깨진 이미지가 태그되는 것을 막는다
- `node` user (uid=1000, gid=1000) — 호스트 `~/.codex` 소유자와 uid 가 일치해야 mount RW 성립
- `ENTRYPOINT []` — node 이미지의 `docker-entrypoint.sh` 가 CMD 를 가로채면 `tail` 이 그대로
  실행되지 않는다
- 컨테이너 `HOME` 이 `/home/node` 이므로 codex 기본 `CODEX_HOME` 이 마운트 경로와 일치한다

사전 준비: `make codex-build` + 호스트에서 `codex` CLI 로그인 (`~/.codex` 생성).

<br>

## 환경 변수

| ENV | Default | 의미 |
|---|---|---|
| `CODEX_AGENT_ENABLED` | `false` | **풀 생성 여부.** false 면 아래 값들은 무시된다 |
| `CODEX_IMAGE` | `issuetracker-codex:local` | 컨테이너 이미지 (`make codex-build`) |
| `CODEX_MODEL` | `gpt-5-codex` | LLM 모델 |
| `CODEX_TIMEOUT` | `120s` | **세션 단위 timeout** — `docker exec codex ...` 1회당 |
| `CODEX_WORKER_COUNT` | `2` | Pool 내 Worker 수 = 동시 컨테이너 수 (상한 16) |
| `CODEX_AUTH_DIR` | `$HOME/.codex` | 호스트 인증 디렉토리 (RW mount) |
| `CODEX_CONTAINER_AUTH_PATH` | `/home/node/.codex` | 컨테이너 내 mount 대상 경로 |

stage prefix 우선순위는 claude 와 동일하다 — `PARSER_CODEX_*` > `CODEX_*` > default
(이슈 #530 의 `agent.StageEnv`).

종료 timeout 은 claude 와 공유한다 (`CLAUDE_CODE_SHUTDOWN_TIMEOUT`) — main 의 정리 루프가
두 backend 의 풀을 같은 상한으로 처리한다.

<br>

## 인증 디렉토리 검증 시점 (이슈 #537)

생성자 (`NewFromEnv` / `New` / `NewWithRunner`) 는 경로를 절대 경로로 정규화만 하고
**파일시스템을 읽지 않는다.** 존재 / 디렉토리 / 읽기 권한 검증은 `Start` 에서 수행한다.

생성자가 순수 데이터 조립이어야 테스트가 실제 디렉토리 없이 Worker 를 만들 수 있고,
특히 `NewWithRunner` 의 DI 의도와 모순되지 않는다. 빈 문자열은 파일시스템 조회 없이 판단
가능한 프로그래밍 오류이므로 **생성 시점 거부를 유지** 한다.

운영 동작은 claude 와 동일하다 — main wiring 이 생성 실패와 Start 실패를 같은 graceful
fallback 으로 처리한다.

<br>

## 의존

- [`pkg/agent`](../../../../pkg/agent/) — `Agent` interface + `StageEnv` + `Backend`
- [`pkg/llm/prompt`](../../../../pkg/llm/prompt/) — prompt loader.
  parser 경로는 claude 와 **같은 asset** 을 쓴다 (`parser/claude/{page,list}.user`) —
  backend 중립이고 placeholder 가 동일하기 때문이며, enrich 도 `enrich/claude/*` 를
  공용한다. 이름 상수는 [`contracts.go`](../../../../pkg/agent/codex/contracts.go) 참조.
- [`pkg/logger`](../logger.md)
- 외부: `docker` CLI (PATH 에 있어야 함), `node:22-bookworm-slim` base image

<br>

## 테스트

[`test/pkg/agent/codex/`](../../../../test/pkg/agent/codex/) — mock `ContainerRunner` 기반,
실제 docker 없음 (이슈 #535).

| 파일 | 범위 |
|---|---|
| `helpers_test.go` | 공용 mock (호출 계수 + exec 훅 + 블로킹 모드) + 생성 헬퍼 |
| `container_test.go` | lifecycle + Stop 실패 후 상태 복원 + exec 인자 형태 + 출력 파싱 |
| `pool_test.go` | round-robin / 부분 기동 실패 정리 / worker count env / stage prefix |
| `session_test.go` | 세션 디렉토리 / path traversal 거부 / MCP 거부 / 타임아웃 |
| `worker_auth_test.go` | authDir 지연 검증 (이슈 #537) |

<br>

## 관련 이슈

- **이슈 #462 — 메타: codex backend 도입**
- 이슈 #532 — Sub 1 골격 (`Worker` / `Pool` / `RunSession`)
- 이슈 #533 — Sub 2 Dockerfile + `make codex-build`
- **이슈 #534 — Sub 3 main wiring + backend 선택 정책** ([README.md](README.md) 참조)
- 이슈 #535 — Sub 4 단위 테스트
- 이슈 #537 — 생성자의 authDir 즉시 검증 제거 (claude 와 공통 적용)
- **이슈 #591 — `--skip-git-repo-check` 누락** (해결)
- **이슈 #594 — parser prompt asset 부재** (해결 — claude asset 공용)
- **이슈 #585 — MCP 지원** (해결 — `-c` override 전달)
- 이슈 #539 — 고아 workspace 정리 (claude 전용)
