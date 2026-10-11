# AI 작업 진행 규약 — 본 프로젝트 전용 사항

공통 규약은 사용자 레벨 하네스 **`~/.claude/rules/`** 에 있다 (2026-10-11 이관, 이슈 #672).
본 저장소의 구 규약 1~8 (이슈 #152 · #199 · #210 · #212 · #665 · #667 · #669) 이 그 원형이다.
이 문서는 **공통 규약이 프로젝트에 위임한 값** 과 **본 저장소만의 명시적 예외** 만 적는다.
공통 규약과 충돌하면 이 문서가 우선한다 — 단, 그 우선권은 여기 적힌 전용 값과 예외에만 미친다. 공통 규약 자체를 바꾸고 싶으면
이 문서에 덮어쓰지 말고 하네스 쪽을 고치고 결정으로 기록한다 (규약 7).

> **하네스가 없는 환경** — GitHub Action [`claude.yml`](../../.github/workflows/claude.yml) 은 저장소만 체크아웃하고 `~/.claude` 를
> 제공하지 않는다 (`anthropics/claude-code-action@v1` 은 저장소의 `CLAUDE.md` · `.claude/` 만 복원한다). 그 환경에서는:
> - **공통 규약 1~7 의 대체본** 은 [CLAUDE.md](../../CLAUDE.md) 의 "AI 작업 진행 규약" 요약과 "PR 생성 후 피드백 대응" 절 (선별 기준 · 커밋 메시지 · 금지 사항 포함) 이다. 이 문서의 전용 사항이 그 위에 더해진다.
> - **저장소 규칙 [01-architecture.md](01-architecture.md) ~ [06-code-style.md](06-code-style.md) 는 그대로 적용된다.** 이 문맥은 공통 *워크플로* 규약의 대체본만 정한다.
> - **규약 8 (서브 에이전트 위임) 은 수행하지 않는다** — Action 은 단일 에이전트로 돌며 티어 → 모델 매핑을 읽을 수 없다. 모든 작업을 T0 (직접) 로 한다.
> - 하네스를 고정 버전으로 Action 에 체크아웃할지는 하네스 저장소의 remote 가 정해진 뒤 별도 결정 (이슈 #672 TODO).

<br>

## 구 규약 번호 → 공통 하네스 대응표

기존 이슈 · PR · 문서 · 스크립트 주석이 "규약 N" 으로 참조한다. 번호는 그대로 쓴다.

| 구 규약 | 내용 | 공통 하네스 (`~/.claude/rules/`) | 본 프로젝트 전용 (아래 절) |
|---|---|---|---|
| 1 | 이슈 먼저 · sub-issue 분할 | `01-workflow.md` 1절, `03-conventions.md` Sub-issue 연결 | — |
| 2 | 자율 진행 · 예외 4영역 | `01-workflow.md` 2절 | 자율 영역 추가 항목 |
| 3 | Commit-per-TODO | `01-workflow.md` 3절 | — |
| 4 | PR 자동 생성 · 생성 후 피드백 | `01-workflow.md` 4절, `06-pr-feedback.md` | 도구 · `claude.yml` |
| 5 | 권한 최소화 | `01-workflow.md` 5절 | — |
| 6 | Label · Issue Type · 접두사 3분리 | `03-conventions.md` | `scripts/gh-meta.sh` · Issue Type ID |
| 7 | 설계 변경 · 의사 결정 기록 (ADR) | `07-decisions.md` | Notion DB |
| 8 | 오케스트레이션 · 역할 군 · 티어 | `02-orchestration.md`, 역할 정의 `~/.claude/agents/` | operator 범위 · PR 작업 분배 표 |

<br>

## 규약 2 — 자율 진행 영역 추가 항목

공통 목록에 더해 다음도 확인 없이 한다:

- **구조 변경 시 `make harness-check` 통과 확인** — cmd 추가/삭제, 디렉토리 이동, Go 버전 변경, CI 임계값 변경 등은
  규약 문서도 같이 고쳐야 한다 (이슈 #539). 문서가 낡으면 다음 세션의 AI 가 잘못된 전제를 받는다.
  harness-check 자체를 고쳤으면 `make harness-selftest` 로 검출력을 확인한다 (이슈 #565) — "경고 0건" 은 통과가 아니다.
- `scripts/` 하위 도구 실행, `make` · `scripts/lint.sh` 실행.
- 의존성 추가 `go get` — 단 신규 외부 모듈은 사전 확인 (규약 5).

<br>

## 규약 4 · 6 — 도구

| 용도 | 수단 | 비고 |
|---|---|---|
| 이슈 Label + Issue Type | `scripts/gh-meta.sh issue <N>` | title prefix 로 자동 판정. 수동 `gh api graphql` 금지 (이슈 #243) |
| PR Label | `scripts/gh-meta.sh pr <N>` | 닫는 이슈의 Label 과 동기화 |
| 리뷰 코멘트 처리 완료 표시 | `scripts/pr-resolve-comments.sh <PR> <comment_id...>` | 👀 + resolve 일괄 — 개별 `gh api` 호출 회피 |
| sub-issue 연결 | `gh api graphql` `addSubIssue` (공통 `03-conventions.md` 의 명령) | 스크립트 없음 — 직접 호출 허용. 규약 6 의 "graphql 직접 호출 금지" 는 Label · Type 에 한정 |
| 세션 종료 후 피드백 | 이슈 · PR 코멘트에 `@claude` → [`claude.yml`](../../.github/workflows/claude.yml) | write 권한 보유자만, 봇은 차단 (이슈 #549) |

Issue Type ID (본 repo): `Feature` → `IT_kwDODsDQh84By0jb` / `Bug` → `IT_kwDODsDQh84By0ja` / `Task` → `IT_kwDODsDQh84By0jZ`.
`scripts/gh-meta.sh` 상수로 관리한다 — 변경 시 스크립트 갱신.

cron loop 는 폐지됐다 (이슈 #548, 구 #129). 폐지된 자산 (`loop.md` · `pr-feedback.md` · `pr-feedback.sh`) 이 되살아나거나
규약이 cron 등록을 지시하면 `make harness-check` 7절이 FAIL 한다.

<br>

## 규약 7 — 결정 기록 위치

- 데이터베이스: [설계 결정 기록](https://app.notion.com/p/90f54429790347b3942b1e13ea34b145) — `최근 PR` 과 같은 `IssueTracker` 페이지 아래 (이슈 #665)
- data source id: `8f5b163d-397a-4182-8b14-2546ffb4a16b` (`scripts/notion-decision.py` 기본값 · `NOTION_DECISION_DS_ID` 로 override)
- **AI 세션**: Notion MCP 로 직접 (`notion-create-pages`, parent = 위 data source)
- **토큰이 있는 환경** (사람의 로컬 셸): `scripts/notion-decision.py add ...` — `notion_pr_sync.py` 와 같은 요청 헬퍼 · 재시도 정책 (이슈 #563)
- CI `@claude` 경로는 `claude.yml` 이 `NOTION_API_TOKEN` 을 step `env` 로 넘기지 않아 **현재는 기록할 수 없다** — 넘길지는 별도 결정 (secret 등록 + 워크플로 변경)
- 두 경로는 같은 DB 에 같은 형식으로 쓴다. 수동 `curl` 은 쓰지 않는다 (이슈 #243 과 같은 원칙)
- 속성 · 본문 형식 (ADR 여섯 항목) 은 공통 `07-decisions.md`. `영역` 값은 저장소 구조를 따른다 — 파이프라인 stage (fetcher / parser / validate / enrich) · storage · bus · llm · agent · ci · 규약

<br>

## 규약 8 — 오케스트레이션 보충

- 역할 군 · 티어 판정 · 티어 → 모델 매핑 · 브리프 · 검수 · 동시 상한은 공통 `02-orchestration.md` 가 **단일 출처** 다.
  역할 정의 파일은 `~/.claude/agents/<역할>.md`. 이 저장소에 같은 이름의 역할 파일을 두면 그쪽이 우선한다 — 지금은 없다.
- **operator** 역할의 본 저장소 범위: migration 적용 (`cmd/migrate` · `cmd/migrate-down`), DLQ 확인, 로그 분석, `cmd/admin` 도구 실행.
  변경은 위임하지 않는다 (규약 2 예외 영역). 읽기 전용 조회 (DLQ 개수 · 로그 검색) 만 T3 로 위임한다.
- 서브 에이전트 브리프의 "제약" 에 반드시 넣는 포인터: 테스트 위치 [05-testing.md](05-testing.md), 로그 메시지 영어
  [04-error-handling.md](04-error-handling.md), 스타일 [06-code-style.md](06-code-style.md).
- 검수 재실행 명령: `make fmt` · `make lint` · `make test` · `make harness-check`.
- PR 본문의 "작업 분배" 섹션은 [PR 템플릿](../../.github/PULL_REQUEST_TEMPLATE.md) 에 있다. 서브 에이전트를 쓰지 않았으면 "없음".
- 도입 배경: 이슈 #667 (티어 · 검수), #669 (역할 군).

<br>

## 참고 자료

- 본 규약 도입 배경: [이슈 #152](https://github.com/EinSofINTEREST/IssueTracker/issues/152)
- 관련 규약:
  - [06-code-style.md](06-code-style.md) — commit/PR 메시지 컨벤션
  - [05-testing.md](05-testing.md) — 작업 단위 테스트 기준
- 관련 이슈:
  - 이슈 #121 — PR 타이틀 lint 정규식
  - 이슈 #129 — PR 생성 직후 cron 자동 등록 (**폐지** — 이슈 #548 로 대체)
  - 이슈 #548 / #549 — 세션 내 Monitor 처리 + GitHub Action 전환
  - 이슈 #199 — 이슈 먼저 (issue-first) 워크플로 명문화 (규약 1 도입)
  - 이슈 #210 — Label · Issue Type · Sub-issue Relation 정책 명문화 (규약 6 도입)
  - 이슈 #212 — 규약 6 의 prefix 표기 체계 3분리 (commit / PR / 이슈) 명문화
  - 이슈 #243 — `scripts/gh-meta.sh` 표준화
  - 이슈 #539 / #565 — `make harness-check` 드리프트 게이트와 selftest
  - 이슈 #665 — 설계 변경·의사 결정 Notion 기록 (규약 7 도입)
  - 이슈 #563 — Notion PR 동기화 (규약 7 이 재사용하는 요청 헬퍼)
  - 이슈 #667 — 메인 세션 오케스트레이션 · 서브 에이전트 모델 차등 배분 (규약 8 도입)
  - 이슈 #669 — 서브 에이전트 역할 군 명시 (규약 8 보완)
  - 이슈 #672 — 공통 규약을 사용자 레벨 하네스 `~/.claude/rules/` 로 이관, 본 문서를 전용 사항으로 재구성
- 관련 문서: [.github/PULL_REQUEST_TEMPLATE.md](../../.github/PULL_REQUEST_TEMPLATE.md)
