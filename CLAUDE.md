# IssueTracker — AI 협업 가이드

이 파일은 AI(Claude, Copilot 등)가 프로젝트를 이해하기 위한 **목차이자 진입점**입니다.
모든 정보를 여기에 담지 않고, 필요한 시점에 해당 문서를 참조하도록 설계했습니다.

## 프로젝트 한 줄 요약

글로벌 뉴스/커뮤니티 이슈를 크롤링 → 임베딩 → 클러스터링하는 Go 기반 파이프라인 시스템.

## 빠른 참조 (목차)

작업 유형별로 필요한 문서만 읽으세요. **전부 읽지 마세요.**

| 작업 | 참조 문서 | 핵심 내용 |
|------|-----------|-----------|
| 코드 스타일 확인 | `.claude/rules/06-code-style.md` | 탭 인덴트, 네이밍, 한국어 커밋 |
| 크롤러 구현 | `.claude/rules/02-crawler-implementation.md` | Crawler 인터페이스, HTTP 클라이언트, 파싱 |
| 데이터 처리 | `.claude/rules/03-data-processing.md` | 정규화 → 검증 → 임베딩 → 클러스터링 |
| 에러 처리 | `.claude/rules/04-error-handling.md` | 에러 타입, 재시도, 로깅 필드 |
| 테스트 작성 | `.claude/rules/05-testing.md` | test/ 디렉토리 구조, 커버리지 (CI 게이트 40%) |
| 아키텍처 이해 | `.claude/rules/01-architecture.md` | 레이어, 디렉토리, 데이터 흐름 |
| **AI 작업 진행** | 공통 하네스 `~/.claude/rules/` + `.claude/rules/07-workflow.md` | **공통: 이슈 먼저 / 자율 진행 / commit-per-TODO / PR / 권한 / Label · Type / 결정 기록 / 오케스트레이션 — 전용: 스크립트 · Notion DB · harness-check** |
| CI/머지 게이트 | `docs/ci/conventions.md` | Required checks, CODEOWNERS, Ruleset |
| Status check 이름 | `docs/ci/status-checks.md` | 단일 소스, 변경 절차 |

## 반드시 지킬 규칙 (CI가 강제함)

이 규칙을 어기면 CI가 실패하여 머지가 차단됩니다. "하지 마"가 아니라 **못 합니다.**

1. **커밋 메시지**: `[FEAT]:` / `[FIX]:` / `[REFAC]:` / `[DOCS]:` / `[CHORE]:` 로 시작. 한국어.
2. **PR 타이틀**: `[카테고리#이슈번호] 제목` (CI 정규식 강제, 이슈 #121). 카테고리는 위 5종, 이슈번호 누락이나 카테고리 오타 시 머지 차단.
3. **gofmt**: `gofmt -w .` 로 포맷 정리 후 커밋.
4. **빌드**: `go build ./...` 통과.
5. **테스트**: `go test -race ./...` 통과. **커버리지 40% 이상** (CI 강제값 — `ci-quality.yml` 의 `threshold`). `05-testing.md` 의 70% 는 core 패키지 목표치이며 CI 가 강제하지 않는다.
6. **린트**: `golangci-lint run` 통과.

## 빌드/테스트 명령어

```bash
make build       # 전체 빌드
make test        # 유닛 테스트
make coverage    # 커버리지 리포트
make lint        # golangci-lint
make fmt         # gofmt
```

## 디렉토리 구조 (요약)

```
cmd/            → 실행 바이너리 (issuetracker, processor, migrate, migrate-down, rule-validator, admin, api)
internal/       → 비공개 비즈니스 로직
pkg/            → 공개 유틸리티 (logger, config, queue, redis)
test/           → 테스트 (internal/, pkg/ 미러링)
docs/ci/        → CI 운영 규약, status check 단일 소스
.claude/rules/  → 상세 개발 규칙 (위 목차 참조)
```

## 작업 전 체크리스트

- [ ] 관련 규칙 문서를 **목차에서 찾아** 읽었는가? (전체 읽기 금지)
- [ ] 커밋 메시지가 `[카테고리]: 한국어 설명` 형식인가?
- [ ] `make fmt && make lint && make test` 를 로컬에서 통과했는가?

## AI 작업 진행 규약

공통 규약은 사용자 레벨 하네스 `~/.claude/CLAUDE.md` · `~/.claude/rules/` 에 있다 (2026-10-11 이관, 이슈 #672).
본 저장소 전용 사항과 구 규약 번호 대응표는 [`.claude/rules/07-workflow.md`](.claude/rules/07-workflow.md).

핵심 8 규약 요약 — `~/.claude` 가 없는 환경 (`claude.yml`) 에서는 이 요약과 07-workflow.md 가 기준이다:

1. **이슈 먼저** — 코드 수정 전 GitHub 이슈. 큰 작업은 메인 + sub-issue N개로 모두 사전 생성, PR 직전 ad-hoc 이슈 금지 (이슈 #199).
2. **자율 진행** — 시스템 변경 / destructive / 외부 영향 / 모호 영역 **네 가지만** 사용자 확인. 그 외는 묻지 않는다 (이슈 #152).
3. **Commit-per-TODO** — 논리적 변경 단위마다 즉시 commit. 몰아서 커밋 금지.
4. **PR 자동 생성** — 템플릿 전 섹션 + `Closes #N` 하나 + Label. base 는 `main`. 머지는 사람이.
5. **권한 최소화** — 새 permission / 외부 도구 / 의존성은 불가피할 때만.
6. **Label · Issue Type 필수** — 이슈 prefix 기준 매핑 (`[FEATURE]→enhancement/Feature`, `[FIX]→bug/Bug`, `[HOTFIX]→bug+hotfix/Bug`, `[REFACTOR]→refactor/Task`, `[CHORE]→chore/Task`, `[DOCS]→documentation/Task`). 부여는 **`scripts/gh-meta.sh issue <N>` / `pr <N>`** — 수동 graphql 금지 (이슈 #210, #212, #243). 표기 3분리: commit `[FEAT]:` / PR `[FEAT#N]` / issue `[FEATURE]`.
7. **결정은 즉시 Notion 에 기록** — [설계 결정 기록](https://app.notion.com/p/90f54429790347b3942b1e13ea34b145) DB 에 한 행, ADR 여섯 항목 (검토한 대안 필수). 아키텍처 · 의존성 · 스키마 · 규약 · 운영 정책 · "두 안 중 하나" 전부. 뒤집힌 결정은 `폐기`/`대체됨` (이슈 #665).
8. **메인 세션은 오케스트레이터** — 소통 · 설계 · 결정 · 통합 · **검수** 는 직접, 닫힌 구현 · 탐색 · 기계적 변경은 역할 (`~/.claude/agents/`) 과 티어 (T0 ~ T3) 로 위임. 서브 에이전트는 커밋 · push · PR · 이슈 · Notion 기록을 하지 않고, 메인이 diff 전수 재독 + 검증 재실행으로 검수한 단위만 커밋. 티어 → 모델 매핑은 `~/.claude/rules/02-orchestration.md` 가 **단일 출처** — 이 파일에 모델 이름을 적지 않는다 (이슈 #667, #669).

## PR 생성 후 피드백 대응 (이슈 #548 — 구 #129 cron 방식 폐지)

절차는 공통 `~/.claude/rules/06-pr-feedback.md`. 본 저장소 전용:

- **세션이 살아 있으면** `Monitor` 로 CI · 코멘트를 감시하고 **그 세션에서** 처리한다 — 작업 맥락이 있어 대응 품질이 가장 높다.
  CI 실패를 코멘트보다 먼저. 처리 완료 표시는 `scripts/pr-resolve-comments.sh <PR번호> <comment_id...>` (👀 + resolve 일괄).
- **세션을 닫은 뒤** 는 이슈 · PR 코멘트에 **`@claude` 멘션** → [`.github/workflows/claude.yml`](.github/workflows/claude.yml) (이슈 #549).
  write 권한 보유자만 트리거, 봇은 차단 — CodeRabbit 피드백은 사람이 확인한 뒤 `@claude` 로 지시한다.
  저장소 규약 (커밋 prefix · PR 타이틀 · 테스트 위치) 은 본 파일을 통해 전달된다.
- 선별 기준은 버그 · 성능 · 보안 · 아키텍처 일관성만. 자동 approve / merge 는 하지 않는다.
- 커밋 메시지: `[FIX]: 피드백 반영, {요약}` / `[FIX]: CI 복구, {job 이름} - {요약}`.
- **cron 자동 등록 금지.** 구 규약 (이슈 #129) 은 `gh pr create` 직후 loop.md (현재 삭제됨) 를 3분 cron 으로 등록했으나 폐지됐다 —
  새 세션이 diff 만 보고 맥락을 잃고, 토큰이 누적되고, 자체 상태 기계가 필요했다. 사용자가 명시적으로 요청하지 않는 한 `CronCreate` 를 호출하지 않는다.
