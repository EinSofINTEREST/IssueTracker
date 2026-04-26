# Required Status Checks — 단일 소스

이 문서는 PR 머지 게이트에 사용되는 Required status check 이름의 **유일한 단일 소스(Single Source of Truth)** 입니다.
GitHub Ruleset과 PR 템플릿은 모두 이 문서의 이름과 **토씨 단위로 일치**해야 합니다.

## 명명 규칙

- **표의 `이름` 열에는 GitHub Ruleset에 실제 등록된 체크 이름을 그대로 기재**한다.
  (GitHub Actions job `name:` 값이 그대로 context가 되므로 Title Case 포함 가능)
- **신규 추가 시 가독성을 위해 Title Case를 허용**하되, 워크플로 간 중복은 금지.
- 리네임 시 머지 게이트가 일시 중단되므로, 기존 체크 이름 변경은 문서/워크플로/Ruleset
  3곳을 같은 PR에서 동시 갱신해야 한다.

## 현재 등록된 체크

| 이름 | 워크플로 / Job | 설명 | Required |
|------|---------------|------|----------|
| `Format Check` | `ci-quality.yml` / `format` | `gofmt -l .` 결과 검증 | Yes |
| `Build` | `ci-quality.yml` / `build` | `go build ./...` 컴파일 검증 | Yes |
| `Test` | `ci-quality.yml` / `test` | `go test -race` + 커버리지 40% 강제 | Yes |
| `Lint` | `ci-quality.yml` / `lint` | `golangci-lint run` (v1.64.8 고정) | Yes |
| `Commit Lint` | `ci-convention.yml` / `commit-lint` | 커밋 메시지 `[카테고리]:` 포맷 강제 | Yes |
| `PR Title Lint` | `ci-convention.yml` / `pr-title-lint` | PR 타이틀 `[카테고리]:` 포맷 강제 (PR only) | Yes |
| `Linked Issue Check` | `ci-convention.yml` / `linked-issue` | PR 에 머지 시 close 될 이슈(closing reference) 가 최소 1개 연결되어 있는지 검증 (`closingIssuesReferences.totalCount ≥ 1`, PR only) | Yes |

## 변경 절차

1. 이 문서를 먼저 업데이트한다.
2. 워크플로의 job name을 문서에 맞춘다.
3. GitHub Ruleset의 "Require status checks to pass" 목록을 문서에 맞춘다.
4. PR 본문에 변경된 체크 이름을 명시한다.

> 이름 불일치는 "머지 영구 차단"의 가장 흔한 원인입니다. 리네임 시 세 곳(이 문서, 워크플로, Ruleset)을 **같은 PR**에서 갱신하세요.
