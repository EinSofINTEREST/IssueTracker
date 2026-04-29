// storage 패키지는 데이터 접근 계층의 인터페이스와 공유 타입을 정의합니다.
// 구현체는 하위 패키지(postgres/)에 위치합니다.
package storage

import (
	"context"

	"issuetracker/internal/crawler/core"
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
	// content_hash가 동일하면 업데이트 없이 기존 레코드를 유지합니다.
	// 3개 테이블에 단일 트랜잭션으로 저장됩니다.
	Save(ctx context.Context, content *core.Content) error

	// SaveBatch는 여러 content를 단일 트랜잭션으로 저장합니다.
	// 일부 실패 시 전체 트랜잭션이 롤백됩니다.
	SaveBatch(ctx context.Context, contents []*core.Content) error

	// GetByID는 ID로 content를 조회합니다 (3테이블 JOIN).
	// 존재하지 않으면 ErrNotFound를 반환합니다.
	GetByID(ctx context.Context, id string) (*core.Content, error)

	// GetByURL은 URL로 content를 조회합니다 (3테이블 JOIN).
	// 존재하지 않으면 ErrNotFound를 반환합니다.
	GetByURL(ctx context.Context, url string) (*core.Content, error)

	// GetByContentHash는 content_hash로 content를 조회합니다 (3테이블 JOIN).
	// 중복 감지에 사용됩니다. 존재하지 않으면 ErrNotFound를 반환합니다.
	GetByContentHash(ctx context.Context, hash string) (*core.Content, error)

	// List는 필터 조건에 맞는 content 목록을 published_at DESC 순으로 반환합니다.
	// 목록 조회이므로 body, image_urls, extra는 포함되지 않습니다 (summary는 포함).
	List(ctx context.Context, filter ContentFilter) ([]*core.Content, error)

	// Count는 필터 조건에 맞는 content 총 개수를 반환합니다 (페이지네이션용).
	Count(ctx context.Context, filter ContentFilter) (int64, error)

	// Delete는 ID로 content를 삭제합니다.
	// ON DELETE CASCADE로 content_bodies, content_meta도 함께 삭제됩니다.
	// 존재하지 않아도 에러를 반환하지 않습니다.
	Delete(ctx context.Context, id string) error

	// ExistsByURL은 해당 URL의 content가 존재하는지 확인합니다.
	ExistsByURL(ctx context.Context, url string) (bool, error)

	// UpdateValidationStatus updates validator result metadata for the content with the given id.
	// If status != ValidationStatusRejected, code/detail are ignored and persisted as NULL.
	// Returns ErrNotFound if the id does not exist.
	// Also refreshes updated_at to NOW() for audit trail consistency.
	//
	// id 기준으로 validator 결과 메타데이터를 갱신합니다 (이슈 #135 / #161).
	// status 가 ValidationStatusRejected 가 아니면 code/detail 은 NULL 로 저장됩니다.
	// 호출은 validator worker 가 contentSvc.Delete 직전에 수행합니다.
	//
	// id 사용 이유 (PR #163 gemini 피드백): url unique 인덱스보다 primary key 가 효율적이고,
	// URL 정규화 정책 (whitelist 기반 query 파라미터 strip) 과 무관하게 매칭이 보장됩니다.
	UpdateValidationStatus(ctx context.Context, id, status, code, detail string) error
}
