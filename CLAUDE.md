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

공통 규약 (이슈 먼저 · 자율 진행 경계 · commit-per-TODO · PR 자동 · 권한 최소화 · Label/Type · 결정 기록 · 오케스트레이션 · PR 피드백) 은
사용자 레벨 하네스 `~/.claude/CLAUDE.md` · `~/.claude/rules/` 가 기본값이다 (2026-10-11 이관, 이슈 #672). 프로젝트 규칙이 다르게 정하면 프로젝트가 우선한다.
본 저장소 전용 사항과 구 규약 번호 대응표는 [`.claude/rules/07-workflow.md`](.claude/rules/07-workflow.md). 요점:

- Label · Issue Type 은 `scripts/gh-meta.sh issue <N>` / `pr <N>` 으로 부여 — 수동 graphql 금지 (이슈 #243)
- 설계 변경 · 의사 결정은 [설계 결정 기록](https://app.notion.com/p/90f54429790347b3942b1e13ea34b145) Notion DB 에 결정 즉시 기록 (이슈 #665)
- 구조 변경 (cmd · 디렉토리 · Go 버전 · CI 임계값) 시 `make harness-check` 통과 (이슈 #539)
- PR 후 피드백은 세션 안에서 `Monitor` 로 처리하고 `scripts/pr-resolve-comments.sh` 로 완료 표시. 세션 종료 후는 이슈 · PR 코멘트에 `@claude` 멘션 → [`.github/workflows/claude.yml`](.github/workflows/claude.yml) (이슈 #548 · #549). cron 자동 등록 금지 (구 #129 폐지)
- `~/.claude` 가 없는 환경 (`claude.yml`) 에는 공통 규약이 로드되지 않는다 — 위 "반드시 지킬 규칙" 과 07-workflow.md 만 적용
