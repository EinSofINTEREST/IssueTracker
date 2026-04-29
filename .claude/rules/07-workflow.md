# AI 작업 진행 규약

이 문서는 Claude / Copilot 등 AI 협업 도구가 본 프로젝트에서 작업을 진행할 때 따라야 하는
**workflow 규약** 입니다. 작업 도중 사용자 승인을 빈번히 요청하여 흐름이 끊기는 문제를
해소하고, AI 의 자율성과 안전성의 균형을 명문화합니다 (이슈 #152).

본 규약은 코드 스타일 / 아키텍처 / 테스트 등 **무엇을 만드느냐** 가 아니라, **어떻게
진행하느냐** 를 다룹니다. 코드 자체의 규약은 [01-architecture.md](01-architecture.md) ~
[06-code-style.md](06-code-style.md) 참조.

<br>

## 핵심 4 규약

### 1. 자율 진행 정책 — 승인 요청 최소화

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

### 2. Commit-per-TODO 정책

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

### 3. PR 자동 생성 정책

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

#### PR 생성 직후 자동 동작 (이슈 #129)

[CLAUDE.md](../../CLAUDE.md#pr-생성-후-자동-동작-이슈-129) 의 자동 동작 — `@.claude/loop.md`
3분 cron 등록. 본 규약은 그 동작이 자동 발동되도록 PR 생성을 보장.

#### 예외

- 작업이 이슈와 무관한 단발성 chore (예: 작은 hotfix, 운영 스크립트) — 사용자가 \"이슈 없이
  PR 올려줘\" 또는 \"이슈 먼저 만들어줘\" 명시
- 작업이 PR 단위가 아닌 운영 명령 (예: migration 적용, log 분석) 만 요청한 경우

<br>

### 4. 권한 사용 최소화

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

## 적용 흐름 (요약)

사용자 요청 도착 →
1. **의도가 명확한가?** Yes → 진행 / No → 구체화 질문 (규약 1 의 모호 영역)
2. **destructive / 시스템 / 외부 영향?** Yes → 사용자 확인 / No → 진행
3. **새 권한 / 외부 의존성 필요?** Yes → 사용자 확인 / No → 진행 (규약 4)
4. **작업 진행** — 논리적 단위마다 commit (규약 2)
5. **작업 완료 → PR 자동 생성** (규약 3) → cron 자동 등록 (이슈 #129)

<br>

## 참고 자료

- 본 규약 도입 배경: [이슈 #152](https://github.com/EinSofINTEREST/IssueTracker/issues/152)
- 관련 규약:
  - [06-code-style.md](06-code-style.md) — commit/PR 메시지 컨벤션
  - [05-testing.md](05-testing.md) — 작업 단위 테스트 기준
- 관련 이슈:
  - 이슈 #121 — PR 타이틀 lint 정규식
  - 이슈 #129 — PR 생성 직후 cron 자동 등록
- 관련 문서: [.github/PULL_REQUEST_TEMPLATE.md](../../.github/PULL_REQUEST_TEMPLATE.md)
