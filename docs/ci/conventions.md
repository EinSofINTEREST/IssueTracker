# CI 운영 규약

저장소 거버넌스와 GitHub Actions CI 설계 규칙을 정의합니다.
이슈 [#85](https://github.com/EinSofINTEREST/IssueTracker/issues/85)의 수용 기준(DoD)을 충족하기 위한 단일 참조 문서입니다.

관련 문서:
- [Required Status Checks 단일 소스](status-checks.md)

---

## 1. PR 머지 게이트 컨벤션

### 1.1 필수 요구사항
- **Required status checks**: 이름은 [status-checks.md](status-checks.md)의 테이블과 완전 일치.
- **Required reviews**: 최소 1명, `Require review from Code Owners` 활성화.
- **Conversation resolution**: 모든 리뷰 코멘트 해결 후에만 머지 가능.
- **Linear history 권장**, Squash 또는 Rebase merge.

### 1.2 Ruleset 우선 원칙
Branch Protection 대신 **Repository Ruleset**을 기본 수단으로 운영합니다.

**이유**:
- 우회 방지: Ruleset은 admin bypass 범위를 명시적으로 제한.
- 세분화 타겟팅: 브랜치 패턴, 태그, 파일 경로별 규칙 분리.
- 감사 추적: 규칙 변경 이력이 별도 이벤트로 기록.

### 1.3 Bot/App 예외 처리
- **화이트리스트 방식**: 허용된 자동화(dependabot, 릴리스 봇 등)만 명시 예외.
- 인적 계정과 동일한 Ruleset 우회 금지 원칙 유지.
- 예외 목록은 [부록 A](#부록-a-허용된-botapp)에서 관리.

---

## 2. CODEOWNERS 전략

### 2.1 원칙
- **SPOF 금지**: 핵심 경로는 개인 + 팀 중복 지정.
- `.github/`, CI 워크플로, 배포 관련 경로는 반드시 CODEOWNERS 커버.
- 전역 `*` 패턴 사용 금지 (머지 병목 방지).
- 부재 시 대체 승인자를 팀 단위로 확보.

---

## 3. GitHub Actions CI 설계 규칙

### 3.1 워크플로 구조
- 단일 워크플로(`ci.yml`)에 모든 CI job을 배치.
- 각 job은 **독립 병렬 실행** (의존 관계 없음 → 빠른 피드백).
- Go module 캐시 활성화 (`actions/setup-go` 의 `cache: true`).

### 3.2 현재 CI Jobs

| Job | 목적 | 실패 시 의미 |
|-----|------|-------------|
| `Format Check` | gofmt 준수 여부 | 코드 포맷 미정리 |
| `Build` | 컴파일 가능 여부 | 빌드 깨짐 — 즉시 수정 |
| `Test` | 유닛 테스트 + race 검출 | 로직 오류 또는 경쟁 조건 |
| `Lint` | 정적 분석 (golangci-lint) | 코드 품질/보안 이슈 |

### 3.3 Job 추가/변경 시 규칙
1. [status-checks.md](status-checks.md) 에 이름 먼저 등록.
2. 워크플로에 job 추가 (이름은 문서와 일치).
3. Ruleset `required_status_checks` 에 등록.
4. PR 템플릿 체크리스트에 추가.
5. **같은 PR**에서 4곳을 동시 갱신.

### 3.4 Failure 처리
- 기본: job 실패 시 PR 머지 차단 (Required check).
- `continue-on-error: true` 사용 금지 (Required check를 우회하게 됨).
- 예외: non-blocking 정보성 job (예: 커버리지 리포트)은 Required에 등록하지 않고
  `if: always()` 로 실행하되 `continue-on-error: true` 허용.

---

## 4. 체크리스트 (PR 리뷰 시 확인)

- [ ] PR 템플릿의 CI 점검 섹션이 누락 없이 작성됨
- [ ] Required status check 이름이 [status-checks.md](status-checks.md)와 일치
- [ ] `continue-on-error: true` 가 Required job에 사용되지 않음
- [ ] CODEOWNERS 변경 시 대체 승인자 포함 여부 확인
- [ ] 워크플로 변경 시 문서/Ruleset 동시 갱신 여부 확인

---

## 부록 A. 허용된 Bot/App

| 계정/App | 용도 | 허용 범위 |
|---------|------|-----------|
| `dependabot[bot]` | 의존성 자동 업데이트 | go.mod / go.sum PR |
| _(추가 시 PR로 이 표 갱신)_ | - | - |
