// Package repository 는 storage 계층의 DB-backed Repository 인터페이스를 정의합니다.
//
// 구현체는 internal/storage/postgres/ 에 위치합니다.
// 호출자는 본 패키지의 인터페이스 타입에 의존하고, 실제 인스턴스는 main.go 에서 wiring 합니다.
//
// 본 패키지는 internal/storage/model 에만 의존합니다.
package repository

import (
	"context"

	"issuetracker/internal/storage/model"
)

// BlacklistRepository 는 parser_blacklist 테이블에 대한 데이터 접근 인터페이스입니다.
//
// goroutine-safe: 모든 구현은 동시 호출 안전해야 함.
type BlacklistRepository interface {
	// Insert 는 새 블랙리스트 row 를 저장합니다. 자연키 (host_pattern, path_pattern) 충돌 시
	// ErrDuplicate 반환. PathPattern 이 빈 문자열이 아니면 RE2 컴파일 검증 — 실패 시 ErrInvalid.
	// 성공 시 r.ID / CreatedAt / UpdatedAt 채워짐.
	Insert(ctx context.Context, r *model.BlacklistRecord) error

	// Update 는 ID 로 row 를 갱신합니다 (자연키 변경 불가). Enabled / Reason / Source 만 변경 가능.
	// 존재하지 않으면 ErrNotFound.
	Update(ctx context.Context, r *model.BlacklistRecord) error

	// Delete 는 ID 로 row 를 삭제합니다. 존재하지 않아도 nil 반환 (idempotent).
	Delete(ctx context.Context, id int64) error

	// GetByID 는 ID 로 row 를 조회합니다.
	GetByID(ctx context.Context, id int64) (*model.BlacklistRecord, error)

	// FindEnabledByHost 는 host_pattern 매칭 enabled=TRUE row 들을 반환합니다 (Matcher 핫패스).
	//
	// 정렬: LENGTH(path_pattern) DESC — 더 구체적인 path 가 먼저 평가되도록 (parser_rules 의
	// FindActiveCandidates 와 동일 정책). path_pattern="" (catch-all) 은 가장 마지막.
	//
	// 매칭 없으면 빈 슬라이스 + nil error.
	FindEnabledByHost(ctx context.Context, host string) ([]*model.BlacklistRecord, error)

	// List 는 필터 조건에 맞는 row 들을 반환합니다 (운영 대시보드용).
	List(ctx context.Context, filter model.BlacklistFilter) ([]*model.BlacklistRecord, error)
}
