// Package types 는 validate 단계의 검증 인터페이스와 결과 타입을 정의합니다.
//
// Sub-package 인 이유 (이슈 #206 후속):
//
//	validate/news + validate/community sub-validator 가 본 타입을 import 하고,
//	validate (parent) 도 dispatch 함수 NewValidator 에서 sub-validator 를 import.
//	타입을 validate (parent) 에 두면 sub → parent import 로 cycle 발생.
//	leaf-only sub-package 인 types/ 는 누구도 import 하지 않으므로 cycle 회피.
//
// import 의존:
//
//	validate (parent)  → types
//	validate/news      → types
//	validate/community → types
//	validate/worker.go → types (validate package 안)
package types

import (
	"context"

	"issuetracker/internal/processor/fetcher/core"
)

// ValidationError 는 단일 필드 검증 실패 정보를 담습니다.
type ValidationError struct {
	Field   string // 검증 실패한 필드명
	Rule    string // 위반된 검증 규칙
	Message string // 사람이 읽을 수 있는 실패 설명
}

// ValidationResult 는 컨텐츠 검증 결과를 담습니다.
type ValidationResult struct {
	IsValid      bool
	QualityScore float32 // 0.0~1.0; 0.5 미만이면 DLQ로 라우팅
	Errors       []ValidationError
}

// Validator 는 SourceType 별로 Content 를 검증하는 인터페이스입니다.
// news, community 등 각 소스 타입별 구현체가 본 인터페이스를 구현합니다.
// 구현체는 goroutine-safe 해야 합니다.
type Validator interface {
	Validate(ctx context.Context, content *core.Content) ValidationResult
}
