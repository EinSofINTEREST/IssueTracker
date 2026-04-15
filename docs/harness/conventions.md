# Harness Engineering 운영 규약

저장소 거버넌스와 Harness 파이프라인 설계 규칙을 정의합니다. 이슈 [#85](https://github.com/EinSofINTEREST/IssueTracker/issues/85)의 수용 기준(DoD)을 충족하기 위한 단일 참조 문서입니다.

관련 문서:
- [Required Status Checks 단일 소스](../ci/status-checks.md)
- [샘플 파이프라인](samples/sample-pipeline.yaml)

---

## 1. PR 머지 게이트 컨벤션

### 1.1 필수 요구사항
- **Required status checks**: 이름은 [status-checks.md](../ci/status-checks.md)의 테이블과 완전 일치.
- **Required reviews**: 최소 1명, `Require review from Code Owners` 활성화.
- **Conversation resolution**: 모든 리뷰 코멘트 해결 후에만 머지 가능.
- **Linear history 권장**, Squash 또는 Rebase merge.

### 1.2 Ruleset 우선 원칙
Branch Protection 대신 **Repository Ruleset**을 기본 수단으로 운영합니다.

**이유**:
- 우회 방지: Ruleset은 admin bypass 범위를 명시적으로 제한.
- 세분화 타겟팅: 브랜치 패턴, 태그, 파일 경로별 규칙 분리.
- 감사 추적: 규칙 변경 이력이 별도 이벤트로 기록.

### 1.3 Robot Account 예외 처리
- **화이트리스트 방식**: 허용된 자동화 작업(릴리스 노트 PR, 자동 라벨링, dependabot류)만 명시 예외.
- 인적 계정과 동일한 Ruleset 우회 금지 원칙 유지.
- 감사 로그 보존 기간: **최소 90일**, 분기별 검토.
- 예외 계정/봇 목록은 이 문서의 [부록 A](#부록-a-허용된-robot-account)에서 관리.

---

## 2. CODEOWNERS 전략

### 2.1 원칙
- **SPOF 금지**: 핵심 경로는 개인 + 팀 중복 지정.
- `.github/`, CI 워크플로, Harness 설정, 배포 관련 경로는 반드시 CODEOWNERS 커버.
- 부재 시 대체 승인자를 팀 단위로 확보.

### 2.2 동기화
- Harness Approver Group과 GitHub CODEOWNERS의 구성원이 **상위 집합 관계**여야 함 (CODEOWNERS ⊆ Approver Group).
- **책임자**: 저장소 owner (현재 @juhy0987).
- **점검 주기**: 분기 1회 정합성 점검, 변경 시 즉시 동기화.

---

## 3. Harness Approval Stage 설계 기준

### 3.1 구조
- Approval은 **단독 Stage**로 배치하며, 병렬 Stage/Step에 넣지 않는다 (실행 순서 명확성).
- 사용 타입: `Harness Approval` (manual).

### 3.2 필수 설정
| 항목 | 값 | 근거 |
|------|----|------|
| `approvers.disallowPipelineExecutor` | `true` | 실행자 자기 승인 차단 (감사 필수) |
| `approvers.minimumCount` | ≥ 2 (prod 기준) | 이중 확인 |
| `timeout` | 24h 이하 권장 | 무한 대기 방지 |
| timeout 초과 시 동작 | **Reject (fail-safe)** | 미결 승인이 배포로 이어지는 것 방지 |
| Approver 그룹 | CODEOWNERS 기반 | RBAC 정합성 |

### 3.3 감사 항목
- 승인자 ≠ 실행자 여부 기록.
- 승인 소요 시간을 DORA의 `Mean Time to Approval`로 집계.

---

## 4. Failure Strategy + Conditional Execution 규칙

### 4.1 우선순위 (핵심 규칙)
1. **실행된 Step의 실패는 Failure Strategy로만 처리한다.**
   - Conditional Execution(`when`)은 Step의 **실행 여부**를 결정하는 게이트이며, 실패 처리 정책이 아니다.
   - `when` 이 false면 Step은 실행되지 않으므로 실패가 발생하지 않는다(=Failure Strategy 평가 대상 아님).
   - `when` 이 true로 평가되어 실행된 Step이 실패한 경우에만 Failure Strategy가 동작한다.
   - **금지 패턴**: 실패 가능성이 있는 Step을 `when` 조건으로 우회시켜 Failure Strategy를 회피하도록 설계하는 것.
2. **가장 좁은 범위(Inner-most scope)가 우선한다.**
   - 적용 순서: `Step` > `Step Group` > `Stage` > `Pipeline`.
   - Step에 Abort, 상위 Step Group에 Retry가 있으면 **Abort가 실행됨.**

예시는 [samples/sample-pipeline.yaml](samples/sample-pipeline.yaml) 참고.

### 4.2 허용 전략
- `Retry`: 네트워크성/일시적 실패.
- `Rollback`: 배포 스테이지에서 표준.
- `Abort`: 데이터 정합성/보안 실패.
- `MarkAsSuccess`: 복구 불가하나 후속 단계가 영향을 받지 않을 때 (사유 필수).
- `Ignore Failure`: **사유 필수 기재, 리뷰 반려 기준 적용.**

### 4.3 금지 패턴
- 근거 없는 `Ignore Failure`.
- `Conditional Execution`으로 실패 스텝을 건너뛰게 만들어 Failure Strategy를 우회하는 설계.
- Stage 레벨에만 Retry를 걸고 Step 실패 정보를 숨기는 설계.

---

## 5. 운영 적용 순서

1. **1차**: 이 문서 + PR 템플릿 + `status-checks.md` 머지.
2. **2차**: `CODEOWNERS` 활성화 및 팀 핸들 확정.
3. **3차**: Harness 파이프라인에 샘플 규칙 반영, 승인 흐름 테스트.
4. **4차**: GitHub Ruleset에서 Required checks / Code Owners review 강제화 (이 시점부터 우회 불가).

---

## 6. 운영 지표 (DORA)

| 지표 | 측정 방법 | 대시보드 |
|------|-----------|----------|
| Change Failure Rate | 배포 후 7일 내 롤백/핫픽스 PR 비율 | TBD (Harness/Grafana) |
| Mean Time to Approval | Approval 요청 → 처리 시간 평균 | TBD |
| Deployment Frequency | 성공 배포 수 / 주 | TBD |
| Mean Time to Recovery | Failure Strategy 발동 → 복구 시점 | TBD |

- 도입 전/후 비교 가능하도록 **적용 직전 4주 baseline**을 수집한다.

---

## 7. 체크리스트 (PR 리뷰 시 확인)

- [ ] PR 템플릿의 Harness 점검 섹션이 누락 없이 작성됨
- [ ] Required status check 이름이 [status-checks.md](../ci/status-checks.md)와 일치
- [ ] Approval Stage가 있다면 `disallowPipelineExecutor: true`
- [ ] `Ignore Failure` 사용 시 사유가 PR 본문에 기재됨
- [ ] Failure Strategy 적용 범위가 PR 본문과 YAML에서 일치
- [ ] CODEOWNERS 변경 시 대체 승인자 포함 여부 확인

---

## 부록 A. 허용된 Robot Account
| 계정 | 용도 | 허용 경로 | 승인자 |
|------|------|-----------|--------|
| _(none)_ | - | - | - |

> 추가 시 PR로 이 표를 갱신하고, 감사 로그 보존 정책을 함께 기재하세요.
