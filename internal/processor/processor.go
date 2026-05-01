// Package processor 는 파이프라인 단계 (fetcher / parser / validate) 의 공통 인터페이스를 정의합니다.
//
// Package processor defines the common Stage interface for pipeline stages
// (fetcher / parser / validate). Each stage owns its Kafka consumer + worker pool
// (and optional auxiliary background goroutines) and is managed uniformly via Stage.
//
// 부수 검증 타입 (Validator / ValidationResult / ValidationError) 은 validate 단계 안의
// news/community sub-validator 가 공유하는 hub 라 본 패키지에 잔류 — `validate/` 로 옮기면
// news/community 가 validate 부모 패키지를 import 하게 되어 import cycle 발생 (이슈 #206).
package processor

import (
	"context"

	"issuetracker/internal/processor/fetcher/core"
)

// Stage 는 파이프라인 단계의 공통 lifecycle 인터페이스입니다 (이슈 #206).
//
// 각 stage 는 Kafka consumer + worker pool 을 보유하며 background goroutine 으로 동작.
// `cmd/issuetracker/main.go` 는 `[]Stage` 로 모든 단계를 균일하게 Start/Stop 관리합니다.
//
// Lifecycle 계약:
//   - Start(ctx): 비-blocking. worker goroutine 기동.
//   - Stop(ctx):  in-flight 작업의 graceful shutdown 대기. ctx timeout 시 강제 반환.
//   - Name():    stage 식별자 ("fetcher" / "parser" / "validate"). 로깅/메트릭/locks.ProcessingKey 에 사용.
//
// 구현체는 goroutine-safe 해야 합니다.
type Stage interface {
	Name() string
	Start(ctx context.Context)
	Stop(ctx context.Context) error
}

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
