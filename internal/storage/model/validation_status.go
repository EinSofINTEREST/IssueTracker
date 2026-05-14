package model

// ValidationStatus values represent the lifecycle state of a validator's content review.
// They map 1:1 to the validation_status column on the contents table.
//
// Transitions: Pending → (validator) → Passed | Rejected
//
//   - Pending  : chain handler 가 raw_contents INSERT 한 직후 기본값
//   - Passed   : validator 통과 (issuetracker.validated 발행 직후)
//   - Rejected : validator maxRetries 영구 실패 (contentSvc.Delete 직전)
const (
	ValidationStatusPending  = "pending"
	ValidationStatusPassed   = "passed"
	ValidationStatusRejected = "rejected"
)
