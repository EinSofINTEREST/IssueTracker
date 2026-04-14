## 연관 이슈
- #

<!--
PR title 예시: [FEAT#issue번호]: 어쩌구저쩌
-->

<br>

## 구현 내용
- 

<br>

## Harness Engineering 점검 (필수)

> 본 섹션은 [Harness 운영 규약](../docs/harness/conventions.md)에 따라 작성합니다.
> 변경이 파이프라인/CI에 영향을 주지 않는 경우에도 항목을 비워두지 말고 `N/A` 사유를 명시하세요.

### 변경 영향 범위
- 영향 파이프라인/스테이지 식별자: 
- 변경 범위(택1): `Stage` / `Step Group` / `Step` / `N/A`
- 위험도(택1): `Low` / `Medium` / `High`

### Approval / 조건 실행
- Approval 게이트 포함 여부: `Yes` / `No`
- JEXL 조건 실행 사용 여부: `Yes` / `No`
  - 사용 시 참조 변수 스코프(account/org/project): 

### Failure Strategy
- 적용 전략(복수 선택): `Retry` / `Rollback` / `Abort` / `MarkAsSuccess` / `Ignore Failure` / `N/A`
- `Ignore Failure` 사용 시 사유(업무 영향 + 대체 검증 근거) **필수 기재**: 
- 적용 범위(가장 좁은 범위 우선 규칙 확인): 

### 검증 / 롤백
- Required status check 이름(단일 소스 [docs/ci/status-checks.md](../docs/ci/status-checks.md)와 일치): 
- 롤백 계획: 

<br>

## TODO
- 

<br>

## 논의 사항
- 

<br>
