package repository

import (
	"context"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage/model"
)

// ContentRepository는 Content CRUD 연산을 위한 데이터 접근 인터페이스입니다.
// 모든 구현체는 goroutine-safe해야 합니다.
// pgx/v5 구현체: internal/storage/postgres/content.go
//
// 내부적으로 3개 테이블을 사용합니다:
//   - contents: 핵심 메타데이터 (핫 경로)
//   - content_bodies: 본문 텍스트 (상세 조회 전용)
//   - content_meta: 확장 메타데이터 (파이프라인 업데이트)
type ContentRepository interface {
	// Save는 content를 저장합니다 (URL 기준 upsert).
	Save(ctx context.Context, content *core.Content) error

	// SaveBatch는 여러 content를 단일 트랜잭션으로 저장합니다.
	SaveBatch(ctx context.Context, contents []*core.Content) error

	// GetByID는 ID로 content를 조회합니다 (3테이블 JOIN). 존재하지 않으면 ErrNotFound.
	GetByID(ctx context.Context, id string) (*core.Content, error)

	// GetByURL은 URL로 content를 조회합니다. 존재하지 않으면 ErrNotFound.
	GetByURL(ctx context.Context, url string) (*core.Content, error)

	// GetByContentHash는 content_hash로 content를 조회합니다 (중복 감지용).
	GetByContentHash(ctx context.Context, hash string) (*core.Content, error)

	// List는 필터 조건에 맞는 content 목록을 published_at DESC 순으로 반환합니다.
	List(ctx context.Context, filter model.ContentFilter) ([]*core.Content, error)

	// Count는 필터 조건에 맞는 content 총 개수를 반환합니다.
	Count(ctx context.Context, filter model.ContentFilter) (int64, error)

	// Delete는 ID로 content를 삭제합니다 (idempotent).
	Delete(ctx context.Context, id string) error

	// ExistsByURL은 해당 URL의 content가 존재하는지 확인합니다.
	ExistsByURL(ctx context.Context, url string) (bool, error)

	// UpdateValidationStatus 는 validator 결과 메타데이터를 id 기준으로 갱신합니다.
	//
	//   - status != ValidationStatusRejected 이면 code/detail 은 NULL 로 저장 (rejected 만 의미 보유).
	//   - 호출은 validate worker 가 contentSvc.Delete 직전에 수행 — 사후 추적 single source 보장.
	//   - updated_at 을 NOW() 로 refresh — audit trail 일관성.
	//   - id 미존재 시 ErrNotFound.
	//
	// id 사용 이유: url unique 인덱스보다 primary key 가 효율적이고, URL 정규화 정책
	// (whitelist 기반 query 파라미터 strip) 과 무관하게 매칭이 보장됩니다.
	UpdateValidationStatus(ctx context.Context, id, status, code, detail string) error
}
