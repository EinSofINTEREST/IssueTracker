# Required Status Checks — 단일 소스

이 문서는 PR 머지 게이트에 사용되는 Required status check 이름의 **유일한 단일 소스(Single Source of Truth)** 입니다.
GitHub Ruleset, Harness 파이프라인 리포트, PR 템플릿은 모두 이 문서의 이름과 **토씨 단위로 일치**해야 합니다.

## 명명 규칙

- **신규 추가되는 체크는** 소문자 + 하이픈만 사용 (`ci-format`, `harness-approval`).
- 워크플로 간 중복 금지 (동일한 job 이름을 두 파일에 두지 않음).
- 리포트 주체가 바뀌어도 이름은 유지 (리포터 변경은 PR + 문서 동시 갱신).
- **레거시 예외**: 이미 Ruleset의 `required_status_checks` 에 등록되어 운영 중인
  체크는 리네임 시 머지 게이트가 일시 중단되는 위험이 있으므로 기존 표기를
  유지한다. 단, 본 표의 `레거시` 컬럼에 명시한다.

## 현재 등록된 체크

| 이름 | 리포터 | 파일/위치 | 설명 | Required | 레거시 |
|------|--------|-----------|------|----------|--------|
| `Format Check` | GitHub Actions | `.github/workflows/ci.yml` (job: `format`) | `gofmt -l .` 결과 검증 | Yes | Yes (대문자/공백 표기 유지) |

## 예정된 체크 (Harness 연동 시 추가)

> 아래는 Harness 파이프라인 적용 후 추가될 예정입니다. 실제 Harness에서 GitHub으로 전송되는 status context 이름과 일치시키세요.

| 이름 | 리포터 | 설명 | Required |
|------|--------|------|----------|
| `harness-ci-build` | Harness | CI 스테이지 빌드/유닛 테스트 | Yes |
| `harness-approval` | Harness | 수동 승인 스테이지 결과 | Yes (prod 배포 PR) |

## 변경 절차

1. 이 문서를 먼저 업데이트한다.
2. 워크플로/Harness 파이프라인 이름을 문서에 맞춘다.
3. GitHub Ruleset의 "Require status checks to pass" 목록을 문서에 맞춘다.
4. PR 본문에 변경된 체크 이름을 명시한다.

> 이름 불일치는 "머지 영구 차단"의 가장 흔한 원인입니다. 리네임 시 세 곳(이 문서, 워크플로/Harness, Ruleset)을 **같은 PR**에서 갱신하세요.
