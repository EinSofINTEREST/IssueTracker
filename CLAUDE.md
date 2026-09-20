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
| **AI 작업 진행** | `.claude/rules/07-workflow.md` | **자율 진행 / commit-per-TODO / PR 자동 / 권한 최소화** |
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
cmd/            → 실행 바이너리 (issuetracker, processor, migrate, migrate-down, rule-validator)
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

## AI 작업 진행 규약 (이슈 #152, #199, #210)

상세는 [`.claude/rules/07-workflow.md`](.claude/rules/07-workflow.md). 핵심 6 규약:

1. **이슈 먼저 생성** — 코드 수정 시작 전 GitHub 이슈 생성. 큰 작업은 메인 + sub-issue N개로 분할 후 모두 사전 생성 (Sub-issue Relation 활성화). PR 직전 ad-hoc 이슈 금지 (이슈 #199).
2. **자율 진행** — 시스템 변경 / destructive 권한 / 외부 영향 / 모호 영역만 사용자 확인. 그 외는 쿼리 의도 기반 자율 진행.
3. **Commit-per-TODO** — 별 언급 없으면 논리적 변경 단위마다 commit (메시지 컨벤션 준수).
4. **PR 자동 생성** — 작업 완료 직후 컨벤션 + 템플릿 준수해서 `Closes #<sub-issue>` 포함 PR 자동 생성. 마지막 sub-issue PR 에서 메인 이슈도 close.
5. **권한 사용 최소화** — 새 permission / 외부 도구 / 의존성은 작업 완수에 불가피한 경우에만.
6. **Label · Issue Type 부여 필수** (이슈 #210, #212) — 이슈는 **issue prefix** 기준 Label + Type (`[FEATURE]→enhancement/Feature`, `[REFACTOR]→refactor/Task`, `[CHORE]→chore/Task`, `[DOCS]→documentation/Task`, `[FIX]→bug/Bug`, `[HOTFIX]→bug+hotfix/Bug`). PR Label 은 그 PR 이 닫는 이슈의 Label 과 동일. **부여 수단: `scripts/gh-meta.sh issue <N>` / `scripts/gh-meta.sh pr <N>` — 수동 `gh api graphql` 대신 항상 이 스크립트 사용** (이슈 #243). 표기 체계 3분리 (commit `[FEAT]:` / PR `[FEAT#N]` / issue `[FEATURE]`) 는 [규약 6](.claude/rules/07-workflow.md) 참조.

## PR 생성 후 피드백 대응 (이슈 #548 — 구 #129 cron 방식 폐지)

PR 을 만든 뒤 CI 결과와 리뷰 코멘트를 처리하는 경로는 **세션 상태에 따라 두 가지** 다.

### 1. 작업 세션이 살아있을 때 — 세션 안에서 직접 처리 (기본)

`Monitor` 로 CI 체크와 신규 코멘트를 감시하고, 이벤트가 도착하면 **그 세션에서** 처리한다.
작업 맥락("왜 이렇게 구현했는지")을 그대로 갖고 있어 대응 품질이 가장 높다.

처리 절차:

1. **CI 실패를 코멘트보다 먼저** 처리한다.
   - 실패 job 식별: `gh pr checks <PR번호>`
   - 로그 수집: `gh run view <runId> --log-failed`
   - 원인 분석 → 수정 → 커밋 → 푸시
2. 리뷰 코멘트 수집: `gh api repos/{owner}/{repo}/pulls/{number}/comments`
   - 👀 리액션이 달린 코멘트는 처리 완료로 건너뛴다
3. **선별 기준** — 다음에 해당하는 피드백만 처리한다:
   - 비즈니스 로직 오류 또는 버그 가능성
   - 성능 최적화 및 보안 강화
   - 아키텍처 일관성 및 클린 코드 원칙

   단순 스타일 차이나 오타 지적은 제외한다.
4. **처리 방식**
   - 의도가 명확한 피드백 → 코드 수정 + 커밋 + 푸시
   - 의도가 불명확한 피드백 → PR 에 질문 코멘트. 질문 대상을 `@` 로 멘션
   - 자동 approve / merge 는 하지 않는다 (브랜치 보호 우회 + prompt injection 위험)
5. **처리 완료 표시** — 일괄 처리:
   ```bash
   scripts/pr-resolve-comments.sh <PR번호> <comment_id1> [<comment_id2> ...]
   ```
   👀 reaction + thread resolve 를 1회 호출로 수행. 개별 `gh api` 호출을 피한다.

커밋 메시지:
- 리뷰 피드백 반영: `[FIX]: 피드백 반영, {변경 요약}`
- CI 실패 복구: `[FIX]: CI 복구, {실패 job 이름} - {변경 요약}`

### 2. 세션을 닫은 뒤 — GitHub Action 에 위임

세션 종료 후 도착하는 리뷰나 다른 사람의 피드백은 `@claude` 멘션으로 GitHub Action 이 처리한다
(이슈 #549). 로컬 세션·머신과 무관하게 동작한다.

세션을 닫기 전에 후속 리뷰가 예상되면 사용자에게 한 줄로 알린다 — PR url 과 "이후 피드백은
`@claude` 멘션으로" 안내.

### 하지 않는 것

- **cron 자동 등록 금지.** 구 규약(이슈 #129)은 `gh pr create` 직후 `.claude/loop.md` 를 3분 주기
  cron 으로 등록했으나 폐지됐다. cron 이 띄우는 새 세션은 PR diff 만 보고 판단해 작업 맥락을
  잃고, 3분마다 세션을 띄워 토큰이 누적되며, 자체 상태 기계(`/tmp` 상태 파일 + 자동 종료 카운터)를
  유지해야 했다. 사용자가 명시적으로 요청하지 않는 한 `CronCreate` 를 호출하지 않는다.
