# Required Status Checks — 단일 소스

이 문서는 PR 머지 게이트에 사용되는 Required status check 이름의 **유일한 단일 소스(Single Source of Truth)** 입니다.
GitHub Ruleset과 PR 템플릿은 모두 이 문서의 이름과 **토씨 단위로 일치**해야 합니다.

## 명명 규칙

- **신규 추가되는 체크는** 소문자 + 하이픈만 사용 (`ci-deploy`, `security-scan`).
- 워크플로 간 중복 금지 (동일한 job name을 두 워크플로에 두지 않음).
- **레거시 예외**: 이미 Ruleset에 등록되어 운영 중인 체크는 리네임 시
  머지 게이트가 일시 중단되므로 기존 표기를 유지한다. 단, 본 표에 명시.

## 현재 등록된 체크

| 이름 | 워크플로 / Job | 설명 | Required | 레거시 |
|------|---------------|------|----------|--------|
| `Format Check` | `ci.yml` / `format` | `gofmt -l .` 결과 검증 | Yes | Yes (대문자/공백 표기 유지) |
| `Build` | `ci.yml` / `build` | `go build ./...` 컴파일 검증 | Yes | Yes |
| `Test` | `ci.yml` / `test` | `go test -race` + 커버리지 | Yes | Yes |
| `Lint` | `ci.yml` / `lint` | `golangci-lint run` | Yes | Yes |

## 변경 절차

1. 이 문서를 먼저 업데이트한다.
2. 워크플로의 job name을 문서에 맞춘다.
3. GitHub Ruleset의 "Require status checks to pass" 목록을 문서에 맞춘다.
4. PR 본문에 변경된 체크 이름을 명시한다.

> 이름 불일치는 "머지 영구 차단"의 가장 흔한 원인입니다. 리네임 시 세 곳(이 문서, 워크플로, Ruleset)을 **같은 PR**에서 갱신하세요.
