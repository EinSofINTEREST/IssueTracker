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
