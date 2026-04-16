# IssueTracker — AI 협업 가이드

이 파일은 AI(Claude, Copilot 등)가 프로젝트를 이해하기 위한 **목차이자 진입점**입니다.
모든 정보를 여기에 담지 않고, 필요한 시점에 해당 문서를 참조하도록 설계했습니다.

## 프로젝트 한 줄 요약

글로벌 뉴스/커뮤니티 이슈를 크롤링 → 임베딩 → 클러스터링하는 Go 기반 파이프라인 시스템.

## 빠른 참조 (목차)

작업 유형별로 필요한 문서만 읽으세요. **전부 읽지 마세요.**

| 작업 | 참조 문서 | 핵심 내용 |
|------|-----------|-----------|
| 코드 스타일 확인 | `.claude/rules/06-code-style.md` | 탭 인들트, 네이밍, 한국어 커밋 |
| 크롤러 구현 | `.claude/rules/02-crawler-implementation.md` | Crawler 인터페이스, HTTP 클라이언트, 파싱 |
| 데이터 처리 | `.claude/rules/03-data-processing.md` | 정규화 → 검증 → 임베딩 → 클러스터링 |
| 에러 처리 | `.claude/rules/04-error-handling.md` | 에러 타입, 재시도, 로깅 필드 |
| 테스트 작성 | `.claude/rules/05-testing.md` | test/ 디렉토리 구조, 커버리지 70% |
| 아키텍처 이해 | `.claude/rules/01-architecture.md` | 레이어, 디렉토리, 데이터 흐름 |
| CI/머지 게이트 | `docs/ci/conventions.md` | Required checks, CODEOWNERS, Ruleset |
| Status check 이름 | `docs/ci/status-checks.md` | 단일 소스, 변경 절차 |

## 반드시 지킬 규칙 (CI가 강제함)

이 규칙을 어기면 CI가 실패하여 머지가 차단됩니다. "하지 마"가 아니라 **못 합니다.**

1. **커밋 메시지**: `[FEAT]:` / `[FIX]:` / `[REFAC]:` / `[DOCS]:` / `[CHORE]:` 로 시작. 한국어.
2. **PR 타이틀**: 동일한 `[카테고리#<issue-number>] ` 접두사 필수.
3. **gofmt**: `gofmt -w .` 로 포맷 정리 후 커밋.
4. **빌드**: `go build ./...` 통과.
5. **테스트**: `go test -race ./...` 통과. 커버리지 70% 이상.
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
cmd/            → 실행 바이너리 (crawler, processor, issuetracker, migrate)
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
