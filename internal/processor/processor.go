// Package processor는 Content 처리 파이프라인의 공통 인터페이스를 정의합니다.
//
// Package processor defines the common interfaces for the content processing pipeline.
// Each stage (validate, enrich, embed) implements ContentProcessor.
package processor

import (
	"context"

	"issuetracker/internal/crawler/core"
)

// ContentProcessor는 Content를 변환하는 파이프라인 단계의 공통 인터페이스입니다.
// validate, enrich, embed 등 각 처리 단계가 이 인터페이스를 구현합니다.
// 구현체는 여러 goroutine에서 동시에 호출되므로 goroutine-safe해야 합니다.
//
// ContentProcessor is the common interface for pipeline stages (validate, enrich, embed).
// Implementations must be safe for concurrent use.
type ContentProcessor interface {
	// Process는 content를 처리하여 변환된 결과를 반환합니다.
	// 처리 실패 시 에러를 반환합니다.
	//
	// Process transforms content and returns the result.
	// Returns an error if processing fails.
	Process(ctx context.Context, content *core.Content) (*core.Content, error)
}

// ValidationError는 단일 필드 검증 실패 정보를 담습니다.
//
// ValidationError holds details of a single field validation failure.
type ValidationError struct {
	Field   string // 검증 실패한 필드명
	Rule    string // 위반된 검증 규칙
	Message string // 사람이 읽을 수 있는 실패 설명
}

// ValidationResult는 컨텐츠 검증 결과를 담습니다.
//
// ValidationResult holds the outcome of content validation.
type ValidationResult struct {
	IsValid      bool
	QualityScore float32          // 0.0~1.0; 0.5 미만이면 DLQ로 라우팅
	Errors       []ValidationError
}

// Validator는 SourceType별로 Content를 검증하는 인터페이스입니다.
// news, community 등 각 소스 타입별 구현체가 이 인터페이스를 구현합니다.
// 구현체는 여러 goroutine에서 동시에 호출되므로 goroutine-safe해야 합니다.
//
// Validator validates Content for a specific source type.
// Implementations must be safe for concurrent use.
type Validator interface {
	Validate(ctx context.Context, content *core.Content) ValidationResult
}
