package repository

import (
	"context"

	"issuetracker/internal/storage/model"
)

// HostScoringRepository 는 host_scoring_state 테이블 접근 인터페이스입니다 (이슈 #382).
//
// 모든 구현체는 goroutine-safe 해야 합니다.
type HostScoringRepository interface {
	// Upsert 는 host 의 점수 상태를 저장합니다. 같은 host 가 있으면 덮어씁니다.
	Upsert(ctx context.Context, state *model.HostScoringState) error

	// Get 은 host 의 점수 상태를 반환합니다. 없으면 storage.ErrNotFound.
	Get(ctx context.Context, host string) (*model.HostScoringState, error)

	// ListAll 은 모든 점수 상태를 반환합니다 — resolver 의 in-memory 스냅샷 hydrate 용.
	//
	// 전체를 읽는 이유: resolver 는 publish hot path 에서 호출되므로 host 마다 DB 를
	// 왕복할 수 없습니다. host 수는 운영 규모상 수백 단위라 전량 적재가 가능합니다.
	ListAll(ctx context.Context) ([]*model.HostScoringState, error)
}
