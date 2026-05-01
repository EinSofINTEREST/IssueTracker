// Package processor 는 파이프라인 단계 (fetcher / parser / validate) 의 공통 lifecycle
// 인터페이스를 정의합니다.
//
// Package processor defines the common Stage interface for pipeline stages
// (fetcher / parser / validate). Each stage owns its Kafka consumer + worker pool
// (and optional auxiliary background goroutines) and is managed uniformly via Stage.
//
// 검증 인터페이스 (Validator / ValidationResult / ValidationError) 는
// `internal/processor/validate/types/` 로 분리됨 (이슈 #206 후속) — sub-validator
// (news / community) 가 import 하므로 leaf-only sub-package 에 두어 cycle 회피.
package processor

import (
	"context"
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
