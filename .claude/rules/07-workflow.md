# AI 작업 진행 규약

이 문서는 Claude / Copilot 등 AI 협업 도구가 본 프로젝트에서 작업을 진행할 때 따라야 하는
**workflow 규약** 입니다. 작업 도중 사용자 승인을 빈번히 요청하여 흐름이 끊기는 문제를
해소하고, AI 의 자율성과 안전성의 균형을 명문화합니다 (이슈 #152).

본 규약은 코드 스타일 / 아키텍처 / 테스트 등 **무엇을 만드느냐** 가 아니라, **어떻게
진행하느냐** 를 다룹니다. 코드 자체의 규약은 [01-architecture.md](01-architecture.md) ~
[06-code-style.md](06-code-style.md) 참조.

<br>

## 핵심 6 규약

### 1. 이슈 먼저 (issue-first) 생성 정책

사용자 작업 지시가 도착하면 **코드 수정 시작 전에 GitHub 이슈를 먼저 생성** 한다 (이슈 #199).

#### 원칙

- 모든 작업은 GitHub 이슈로 추적 — branch / commit / PR 모두 그 이슈를 참조
- **이슈 생성 시 Label · Issue Type 부여 필수** (규약 6 매핑 표 참조)
- **규모가 PR 1개로 reviewable diff 안 되면 메인 이슈 + sub-issue N개로 분할** 하여 모두 사전에 생성
  - 메인 이슈 본문에 전체 그림 + sub-issue 목록 + 완료 조건 명시
  - **GitHub 의 Sub-issue 기능 + Relation 적극 활용** — `gh api graphql` 의 `addSubIssue` mutation 으로 메인 ↔ sub 관계 활성화하여 GitHub UI 에서 계층 명시
  - 본문 link (`Parent: #<메인>`) 만으로 표기하던 기존 방식은 보조용. native sub-issue 가 우선
- 각 sub-issue 단위로 `branch → 작업 → commit → PR` 사이클 반복
- PR closing reference 는 그 sub-issue (`Closes #<sub>`). 메인 이슈는 모든 sub-issue 가 close 될 때까지 OPEN 유지하고 마지막 sub-issue PR 에서 함께 close

#### Why

- 작업 진입 전 사용자와 scope 합의가 강제됨 → 작업 도중 방향 이탈 / scope creep 회피
- ad-hoc 으로 PR 직전에 이슈 만드는 패턴 차단 (기존 사례: PR #181 / #183 / #189 / #191 — closing reference 가 필요해서 PR 직전 즉흥 sub-issue 생성)
- 메인 이슈와 sub-issue 의 계층 관계가 GitHub 상에서 명확히 link

#### 예외

- 사용자가 명시적으로 **"이슈 없이 진행해"** / **"단발 hotfix"** 라고 지시한 경우
- 1줄 typo 수정 / 명백한 작은 chore (사용자 동의 없이 진행해도 손실 적음 — 단, PR 타이틀 규약 준수를 위해 작업 시작 전 이슈 생성)

#### 판단 모호 시

규모가 작아 보이더라도 **이슈 1개 생성** — 작업 끝난 후 PR 본문 정리할 때 추가 비용 거의 없음.
"이거 큰가?" 가 50/50 이면 **메인 + sub-issue 분할 쪽** 으로 보수 분류.

#### Sub-issue 등록 명령

```bash
# 메인 이슈에 sub-issue 등록 (Relation 자동 활성화)
MAIN_ID=$(gh issue view <MAIN_NUMBER> --json id --jq .id)
SUB_ID=$(gh issue view <SUB_NUMBER> --json id --jq .id)

gh api graphql -f query='
mutation($issueId: ID!, $subIssueId: ID!) {
  addSubIssue(input: {issueId: $issueId, subIssueId: $subIssueId}) {
    issue { number }
    subIssue { number }
  }
}' -f issueId="$MAIN_ID" -f subIssueId="$SUB_ID"
```

<br>

### 2. 자율 진행 정책 — 승인 요청 최소화

쿼리의 의도가 명확하면 AI 는 **사용자 승인 없이 진행** 한다. 다음 4가지 영역만 예외로
사용자 확인을 받는다:

#### 예외 영역 (반드시 사용자 확인)

| 영역 | 예시 |
|---|---|
| **시스템 자체 변경** | OS / 패키지 / 글로벌 환경 변경 (`apt-get install`, `sudo systemctl`, 시스템 서비스 enable 등) |
| **언급 없는 destructive 권한** | `git push --force`, branch 삭제, DB `DROP TABLE`, `git reset --hard`, 무인 PR merge |
| **외부 시스템 영향** | PR merge / issue close / 배포 트리거 / 외부 API 비용 결제 / 외부 DM 발송 |
| **모호한 작업 범위** | 사용자 의도가 다중 해석 가능하거나 scope 가 불명확한 경우 — 진행 전 구체화 질문 |

#### 자율 진행 영역 (승인 불필요)

- 코드 작성 / 수정 / 리팩토링 / 삭제
- 테스트 추가 / 갱신
- 새 파일 / 디렉토리 생성
- DB migration 작성 (단 `DROP` 류는 예외 — 위 destructive 영역)
- 의존성 추가 (`go get`) — 단 신규 외부 모듈은 규약 4 적용
- Branch 생성, 정상 push (force-push 아닌)
- Commit 단위 결정
- PR 본문 작성

#### 판단 모호 시
\"이게 destructive 영역인가?\" 가 50/50 이면 **사용자 확인 쪽으로 보수 분류**. 한 번의 짧은
확인이 잘못된 진행 후 롤백 비용보다 작다.

<br>

### 3. Commit-per-TODO 정책

별다른 사용자 언급이 없으면, AI 는 작업을 **논리적 변경 단위 (TODO)** 마다 commit 한다.

#### 원칙

- 큰 PR 도 reviewable diff 단위로 분할 commit
- 각 commit 메시지는 [06-code-style.md](06-code-style.md) 의 컨벤션 준수:
  - **Prefix 는 다섯 가지 중 택 1**: `[FEAT]:` / `[FIX]:` / `[REFAC]:` / `[DOCS]:` / `[CHORE]:`
  - 이후 한국어 + 변경 의도
  - 단일 commit 이 너무 큰 변경을 담지 않도록
- 빌드 그린 유지 — 각 commit 이 컴파일 + 테스트 통과 가능한 상태

#### 예외

- 사용자가 명시적으로 \"한 번에 묶어줘\" / \"squash 해줘\" 요청 시 단일 commit
- 사용자가 \"단일 fix 만 해줘\" 등 명백한 단일 변경을 지시한 경우

#### 예시

```bash
git log --oneline (refactor/#148 PR 의 5 commits 패턴)
fc95aec [FIX]: 피드백 반영, all-pass 모드 PathPrefixes 검증 + discovery vs fallback 명확 구분
2a063e8 [FIX]: 피드백 반영, LinkDiscoveryConfig 주석 + migration 008 주석 일관성
6491d20 [REFAC]: MaxLinksPerPage 정책 변경 — same-origin 무제한 + cross-origin random sample
4cfc9a4 [FEAT]: migration 008 — 운영 사이트 list rules all-pass 모드 + ExcludePatterns 강화
9f7cbb6 [FEAT]: discovery 테스트 갱신 — all-pass 모드 케이스 추가
```

<br>

### 4. PR 자동 생성 정책

작업 완료 직후 (별다른 언급 없으면) AI 는 **PR 을 자동 생성** 한다.

#### 컨벤션 + 템플릿 준수

- **PR 타이틀**: `[카테고리#이슈번호] 제목` 정규식 (이슈 #121, CI 강제)
- **본문**: [.github/PULL_REQUEST_TEMPLATE.md](../../.github/PULL_REQUEST_TEMPLATE.md) 의 모든 섹션 채움
  - 연관 이슈 (`Closes #N` — closing reference 명시)
  - 구현 내용
  - CI / 머지 게이트 점검
  - 변경 영향 범위 + 위험도
  - 롤백 계획
- **이슈 링크**: PR 본문 또는 Development sidebar 에 closing reference
- **Label 부여 필수**: 규약 6 매핑 표에 따라 PR 에도 동일 label 부여 (`gh pr create --label <label>` 또는 생성 직후 `gh pr edit --add-label`)

#### PR 생성 직후 자동 동작 (이슈 #129)

[CLAUDE.md](../../CLAUDE.md#pr-생성-후-자동-동작-이슈-129) 의 자동 동작 — `@.claude/loop.md`
3분 cron 등록. 본 규약은 그 동작이 자동 발동되도록 PR 생성을 보장.

#### 예외

- 작업이 이슈와 무관한 단발성 chore (예: 작은 hotfix, 운영 스크립트) — 사용자가 \"이슈 없이
  PR 올려줘\" 또는 \"이슈 먼저 만들어줘\" 명시
- 작업이 PR 단위가 아닌 운영 명령 (예: migration 적용, log 분석) 만 요청한 경우

<br>

### 5. 권한 사용 최소화

자율 진행 시, **꼭 필요한 경우가 아니면 이미 허용된 권한 범위 내에서만 동작** 한다.

#### 원칙

- 새 `Bash(...)` permission 요청은 작업 완수에 불가피한 경우에만
- 동등 효과를 낼 수 있는 기존 허용 도구가 있으면 그것을 우선 사용 — 새 외부 도구 설치 X,
  기존 `gh` / `go` / `git` / `make` 등 활용
- `WebFetch` / `WebSearch` 도 새 도메인은 작업 명시적 필요 시에만
- 신규 외부 의존성 (Go module / system package) 추가는 **규약 1 의 \"모호 영역\"** 으로 간주
  → 사용자 사전 확인

#### 이유

- 누적 권한이 늘어날수록 `.claude/settings.local.json` 의 entries 가 비대해져 정리 비용 증가
- 정리 사례: 86 entries → 30 entries (이슈 #152 작업 직전 정리)
- 잘못된 도구 도입은 보안 노출 위험 증가 (예: 토큰 노출, 시스템 파괴 명령)

<br>

### 6. 이슈 / PR 분류 메타데이터 정책 — Label · Issue Type

이슈 / PR 생성 시 **항상 Label 부여** (필수). 이슈는 추가로 **Issue Type 부여** (필수).
분류·필터링·자동화 (예: hotfix 라벨로 우선순위 알림) 가 누락되지 않도록 일관 매핑 적용.

#### Label 매핑 (이슈 + PR 공통)

| Commit prefix | 기본 Label | 추가 Label (조건부) |
|---|---|---|
| `[FEATURE]` | `enhancement` | — |
| `[REFACTOR]` | `refactor` | — |
| `[CHORE]` | `chore` | — |
| `[DOCS]` | `documentation` | — |
| `[FIX]` (일반 에러 이슈) | `bug` | — |
| `[HOTFIX]` (배포 중 긴급) | `bug` | + `hotfix` |

#### Issue Type 매핑 (이슈 전용)

GitHub Issue Type 은 라벨과 별개의 native 분류 — `gh api graphql` 의 `updateIssueIssueType` mutation 으로 부여.

| Commit prefix | Issue Type |
|---|---|
| `[FEATURE]` | `Feature` |
| `[FIX]` | `Bug` |
| `[REFACTOR]` / `[CHORE]` / `[DOCS]` | `Task` |

본 repo 의 Issue Type ID:
- `Feature` → `IT_kwDODsDQh84By0jb`
- `Bug` → `IT_kwDODsDQh84By0ja`
- `Task` → `IT_kwDODsDQh84By0jZ`

#### 부여 명령 예시

**이슈 생성 시 Label**:
```bash
gh issue create --repo EinSofINTEREST/IssueTracker \
  --title "[DOCS] 제목" \
  --label documentation \
  --body "..."
```

**이슈 Type 부여 (생성 직후)**:
```bash
ISSUE_ID=$(gh issue view <ISSUE_NUMBER> --json id --jq .id)

gh api graphql -f query='
mutation($issueId: ID!, $issueTypeId: ID!) {
  updateIssueIssueType(input: {issueId: $issueId, issueTypeId: $issueTypeId}) {
    issue { number issueType { name } }
  }
}' -f issueId="$ISSUE_ID" -f issueTypeId="IT_kwDODsDQh84By0jZ"
```

**PR 생성 시 Label**:
```bash
gh pr create --label documentation --title "[DOCS#N] 제목" --body "..."
# 또는 생성 후
gh pr edit <PR_NUMBER> --add-label documentation
```

#### Why

- Label 누락 시 GitHub Issues / PR 필터링이 무력화 — `is:issue label:bug` 같은 운영 쿼리가 불완전
- Issue Type 은 native 분류로, label 보다 강한 시멘틱 (Project Roadmap 의 Type 컬럼 자동 반영)
- `hotfix` 라벨은 배포 게이트 우선순위 알림 / Slack 라우팅 트리거에 활용 가능

#### How to apply

- 이슈 생성 후 즉시 Label + Type 둘 다 부여 (생성 직후 같은 회차에서 처리, 까먹지 않도록)
- PR 생성 시 `--label` 플래그로 같이 지정 (생성 후 add-label 도 OK)
- 매핑이 모호하면 (예: refactor 인데 bug 도 같이 잡는 PR) 가장 큰 변경 의도 prefix 기준으로 분류 + 보조 label 추가 가능

<br>

## 적용 흐름 (요약)

사용자 요청 도착 →
1. **의도가 명확한가?** Yes → 진행 / No → 구체화 질문 (규약 2 의 모호 영역)
2. **이슈 생성 + Label/Type 부여** (규약 1 + 규약 6) — 단발은 이슈 1개 / 큰 작업은 메인 + sub-issue N개로 분할 후 모두 사전 생성 (sub-issue Relation 활성화). 모든 이슈에 Label · Issue Type 부여. "이슈 없이 진행해" 명시 시 skip
3. **destructive / 시스템 / 외부 영향?** Yes → 사용자 확인 / No → 진행
4. **새 권한 / 외부 의존성 필요?** Yes → 사용자 확인 / No → 진행 (규약 5)
5. **작업 진행** — sub-issue 단위로 branch / 논리 단위마다 commit (규약 3)
6. **작업 완료 → PR 자동 생성 + Label 부여** (규약 4 + 규약 6) — `Closes #<sub-issue>` 명시, 마지막 sub-issue PR 에서 메인 이슈도 close → cron 자동 등록 (이슈 #129)

<br>

## 참고 자료

- 본 규약 도입 배경: [이슈 #152](https://github.com/EinSofINTEREST/IssueTracker/issues/152)
- 관련 규약:
  - [06-code-style.md](06-code-style.md) — commit/PR 메시지 컨벤션
  - [05-testing.md](05-testing.md) — 작업 단위 테스트 기준
- 관련 이슈:
  - 이슈 #121 — PR 타이틀 lint 정규식
  - 이슈 #129 — PR 생성 직후 cron 자동 등록
  - 이슈 #199 — 이슈 먼저 (issue-first) 워크플로 명문화 (규약 1 도입)
  - 이슈 #210 — Label · Issue Type · Sub-issue Relation 정책 명문화 (규약 6 도입)
- 관련 문서: [.github/PULL_REQUEST_TEMPLATE.md](../../.github/PULL_REQUEST_TEMPLATE.md)
