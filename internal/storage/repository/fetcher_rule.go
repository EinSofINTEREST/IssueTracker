package repository

import (
	"context"

	"issuetracker/internal/storage/model"
)

// FetcherRuleRepository 는 fetcher_rules 테이블에 대한 데이터 접근 인터페이스입니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — Resolver 가 핫패스에서 GetByHost 를 호출.
type FetcherRuleRepository interface {
	// Upsert 는 host_pattern 단위 UPSERT 입니다.
	Upsert(ctx context.Context, host string, fetcher model.FetcherKind, reason string) error

	// GetByHost 는 host_pattern 으로 단일 row 를 조회합니다. 매칭 없으면 ErrNotFound.
	GetByHost(ctx context.Context, host string) (*model.FetcherRuleRecord, error)

	// List 는 모든 fetcher_rules 를 host_pattern ASC 로 반환합니다 (운영 도구용).
	List(ctx context.Context) ([]*model.FetcherRuleRecord, error)

	// Delete 는 host_pattern 으로 row 를 제거합니다 (idempotent).
	Delete(ctx context.Context, host string) error

	// BulkDowngradeAutoUpgraded 는 자동 upgrade 된 모든 host 를 goquery 로 일괄 다운그레이드합니다.
	// 변경된 host_pattern 슬라이스를 반환.
	BulkDowngradeAutoUpgraded(ctx context.Context) ([]string, error)
}
