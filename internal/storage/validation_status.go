// Package storage — validation status lifecycle constants (이슈 #135 / 이슈 #161).
package storage

// ValidationStatus values represent the lifecycle state of a validator's content review.
// They map 1:1 to the validation_status column on the contents table.
//
// Transitions: Pending → (validator) → Passed | Rejected
//
// validator 처리 결과의 라이프사이클을 나타냅니다.
//
//   - Pending  : chain handler 가 raw_contents INSERT 한 직후 기본값
//     (parser worker 가 contents INSERT 시 default 'pending' 그대로 유지)
//   - Passed   : validator 통과 (issuetracker.validated 발행 직후)
//   - Rejected : validator maxRetries 영구 실패 (contentSvc.Delete 직전)
//
// 본 상수는 contents 테이블의 validation_status 컬럼 값과 1:1 매핑됩니다 (이슈 #161 도메인
// 중립화로 news_articles 에서 contents 로 이전).
const (
	ValidationStatusPending  = "pending"
	ValidationStatusPassed   = "passed"
	ValidationStatusRejected = "rejected"
)
