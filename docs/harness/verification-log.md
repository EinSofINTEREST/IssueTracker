# Harness 연동 검증 로그

이슈 [#87](https://github.com/EinSofINTEREST/IssueTracker/issues/87) 후속 검증 결과를 누적 기록합니다.

## 검증 항목 정의

| 항목 | 합격 기준 |
|------|-----------|
| PR Trigger 발사 | main 대상 PR 생성 시 Harness 파이프라인 실행 시작 |
| `harness-ci-build` status 전송 | PR Checks 탭에 context 노출 + pending → success/failure 전이 |
| `harness-approval` status 전송 | 승인/거부/타임아웃 시 각각 success/failure 전이 |
| Ruleset 매칭 | 등록 후 PR이 status 미통과 시 머지 차단 |

## 실행 기록

> 검증을 진행하며 한 행씩 추가합니다.

| 일자 | PR | 시나리오 | 결과 | 비고 |
|------|----|---------|------|------|
| _pending_ | #(임시 PR) | 최초 트리거 / status 전송 | _대기_ | 본 PR 머지 후 갱신 |
