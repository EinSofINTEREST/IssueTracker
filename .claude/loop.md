# PR 피드백 순환 처리

## 대상
현재 브랜치에 연결된 열린 PR의 CI 상태와 리뷰 코멘트를 처리한다.

## 절차
1. `gh pr view --json number,url,statusCheckRollup` 로 현재 PR 과 CI rollup 을 함께 조회
2. **CI 실패 확인 (코멘트 처리보다 우선)**
   - `statusCheckRollup` 항목 중 `conclusion == "FAILURE"` 인 GitHub Actions check_run 이 있으면, 실패 job 의 로그를 수집해 우선 복구
     - 실패 job 식별: `gh pr checks <PR번호>` 로 이름·URL 조회
     - 로그 수집: `gh run view <runId> --log-failed` (Actions run id 가 URL 에 포함됨)
     - 원인 분석 → 코드/설정 수정 → 커밋 → 푸시
     - 푸시 직후 본 단계 종료. CI 재실행 결과는 다음 회차에서 재확인 (세션 유지 회피)
   - `IN_PROGRESS` / `QUEUED` / `PENDING` 만 있고 FAILURE 가 없으면 코멘트 처리는 계속 진행 (다음 회차에서 결과 재확인)
   - 모두 `SUCCESS` / `NEUTRAL` / `SKIPPED` 면 정상 진행
3. `gh api repos/{owner}/{repo}/pulls/{number}/comments` 로 리뷰 코멘트 수집
4. 👀 리액션이 달린 코멘트는 처리 완료로 건너뛴다
5. 새 코멘트가 없으면 "새 피드백 없음" 출력 후 종료

## 선별 기준
다음에 해당하는 피드백만 처리한다:
1. 비즈니스 로직 오류 또는 버그 가능성
2. 성능 최적화 및 보안 강화
3. 아키텍처 일관성 및 클린 코드 원칙

단순 스타일 차이나 오타 지적은 제외한다.

## 처리 방식
- 의도가 명확한 피드백 → 코드 수정 + 커밋 + 푸시
- 의도가 불명확한 피드백 → PR에 질문 코멘트를 남김
  - 질문 시, 질문 주체를 "@"를 통해 언급해주어야 함
- 처리 완료한 코멘트에 👀 리액션 추가, Resolve conversation

## 커밋 규칙
- 메시지 형식
  - 리뷰 피드백 반영: `[FIX]: 피드백 반영, {변경 요약}`
  - CI 실패 복구: `[FIX]: CI 복구, {실패 job 이름} - {변경 요약}`
- 한국어로 작성

## 자동 중단 (4회 연속 무동작 시)

세션 종료 후 사용자 개입 없이도 idle 한 루프를 자체 정리하기 위한 단계입니다.
회차의 마지막 단계로 항상 수행합니다.

### 1. 상태 파일
경로: `.claude/loop-state.json` (gitignore 대상 — 세션/체크아웃 로컬 상태)

스키마:
```json
{
  "<PR번호>": {
    "idle_streak": 0,
    "last_run_at": "2026-04-28T03:50:00Z"
  }
}
```

PR 번호는 1단계의 gh pr view 결과에서 얻은 number를 사용합니다.
파일이 없거나 해당 PR 키가 없으면 idle_streak=0 으로 시작합니다.

### 2. 회차 분류

본 회차가 둘 중 어느 카테고리에 해당하는지 결정:

**active (의미 있는 작업 발생 — 카운터 0 으로 reset)**
- CI 실패 복구로 commit + push
- 리뷰 피드백 반영으로 commit + push
- 새 질문 코멘트 작성
- 신규 코멘트에 👀 reaction + thread resolve

**idle (무동작 — 카운터 +1)**
- "새 피드백 없음" 출력으로 종료
- CI 가 IN_PROGRESS / QUEUED / PENDING 만 있고 신규 처리 대상 없음
- 기존 thread 모두 resolved 상태에서 신규 코멘트가 단순 동의/확인 답변뿐 (👀 만 부여, 코드 변경 0)

판단 모호 시 active 로 분류 (보수적으로 streak 0 reset).

### 3. 카운터 갱신 + 종료 판단

회차 분류 결정 직후:

1. 상태 파일 read (없으면 빈 객체로 시작)
2. 본 PR 의 `idle_streak` 갱신
   - active → `0`
   - idle → `+1`
3. `last_run_at` 을 현재 ISO8601 시각으로 갱신
4. 상태 파일 write
5. **`idle_streak >= 4` 인 경우 자동 종료 절차 진입**

### 4. 자동 종료 절차

조건 충족 시:

1. `CronList` 로 활성 cron 조회
2. prompt 에 본 PR 번호가 포함된 loop cron 식별 (예: `"PR #128"` substring 매칭)
3. 매칭된 cron 의 ID 로 `CronDelete` 호출
4. 본 PR 의 상태 항목을 상태 파일에서 제거 (다음 수동 시작 시 fresh state)
5. 사용자에게 한 줄 알림: `"4회 연속 무동작으로 PR #N loop 자동 종료 (cron <id>)"`

매칭되는 cron 이 없으면 (사용자가 이미 수동으로 중단했거나 다른 식별자 사용) 알림만 출력하고 종료.

### 5. 예외
- 사용자가 동일 PR 에 대해 명시적으로 새 작업을 지시하면 (loop 외부 prompt) 자동 카운터와 무관 — 다음 loop 회차 진입 시 자연스럽게 active 로 분류되어 0 으로 reset 됨
- 상태 파일 read/write 실패는 WARN 로그만 남기고 진행 (자동 중단 기능은 best-effort)
