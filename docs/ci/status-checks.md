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
| `PR Title Lint` | `ci-convention.yml` / `pr-title-lint` | PR 타이틀 `[카테고리#이슈번호] 제목` (또는 `[카테고리#이슈번호]: 제목`) 엄격 강제 (이슈 #121, PR only) | Yes |
| `Linked Issue Check` | `ci-convention.yml` / `linked-issue` | PR 에 머지 시 close 될 이슈(closing reference) 가 최소 1개 연결되어 있는지 검증 (`closingIssuesReferences.totalCount ≥ 1`, PR only) | Yes |
| `Harness Check` | `ci-convention.yml` / `harness-check` | 규약 문서가 저장소 실체와 맞는지 대조 + 검출력 셀프테스트 (이슈 #539, #565, #567) | No |

> ⚠️ `Harness Check` 는 워크플로에는 추가됐지만 **Ruleset 의 required 목록에는 아직 등록되지
> 않았습니다.** 등록 전까지는 실행은 되나 실패해도 머지를 막지 못합니다. 등록은 저장소 설정
> 변경이라 소유자 권한이 필요합니다 — 아래 "변경 절차" 3번 참조.

### `Harness Check` 의 실패 기준

| 대상 | 동작 | 이유 |
|---|---|---|
| cmd 목록 · Go 버전 · 커버리지 임계값 · status check 이름 · prompt asset · 폐지 자산 재등장 | **job 실패** | 사실 대조라 오탐이 없다 |
| 문서가 언급한 경로 부재 | **경고만** (Summary 에 표시) | 코드 블록의 예시 경로와 목표 상태 서술을 기계적으로 구분할 수 없어 사람이 판단한다 (이슈 #539 설계) |
| 셀프테스트 (`harness-check-selftest.sh`) | **job 실패** | 검사기 자신이 드리프트를 놓치면서 "경고 0건" 을 유지하는 무력화를 막는다 (이슈 #565) |

경로 경고를 머지 차단으로 승격하려면 `scripts/harness-check.sh` 의 `missing_paths` 를
`fail_count` 에 반영해야 한다. 목표 구조를 서술하는 문서마다 코드 블록 첫 줄에
`# ↓ 목표 구조 — ...` 표기가 필요해지므로, 승격 전에 그 비용을 감안할 것.

## 변경 절차

1. 이 문서를 먼저 업데이트한다.
2. 워크플로의 job name을 문서에 맞춘다.
3. GitHub Ruleset의 "Require status checks to pass" 목록을 문서에 맞춘다.
4. PR 본문에 변경된 체크 이름을 명시한다.

> 이름 불일치는 "머지 영구 차단"의 가장 흔한 원인입니다. 리네임 시 세 곳(이 문서, 워크플로, Ruleset)을 **같은 PR**에서 갱신하세요.
