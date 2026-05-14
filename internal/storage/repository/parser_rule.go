package repository

import (
	"context"

	"issuetracker/internal/storage/model"
)

// ParserRuleRepository 는 parser_rules 테이블에 대한 데이터 접근 인터페이스입니다.
//
// ParserRuleRepository is the data access interface for parser_rules.
// All implementations must be goroutine-safe.
type ParserRuleRepository interface {
	// Insert 는 새 규칙을 저장합니다. 자연키 충돌 시 ErrDuplicate 반환.
	// 성공 시 r.ID 가 채워집니다.
	Insert(ctx context.Context, r *model.ParserRuleRecord) error

	// Update 는 ID 로 규칙을 갱신합니다. 존재하지 않으면 ErrNotFound 반환.
	// 갱신 가능 필드: Selectors, Confidence, Enabled, Description, Article (자연키 + PageType 은 변경 불가).
	Update(ctx context.Context, r *model.ParserRuleRecord) error

	// UpdatePathPattern 은 정밀화 워크플로 에서 호출 — path_pattern + description 갱신.
	//
	// pattern 이 비어있지 않으면 RE2 컴파일 검증 (Insert 와 동일 정책) — 실패 시 ErrInvalid.
	//
	// optimistic guard: 대상 rule 이 여전히
	// (source_name='llm-auto' AND enabled=TRUE AND path_pattern='') 상태일 때만 적용.
	// 가드 실패 또는 rule 미존재 시 ErrNotFound — 호출자가 lost-update 회피용으로 분기.
	//
	// description 은 정밀화 시각 / 방식 (algorithm / llm) 등 추적용 history append.
	UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error

	// GetByID 는 ID 로 규칙을 조회합니다.
	GetByID(ctx context.Context, id int64) (*model.ParserRuleRecord, error)

	// FindActive 는 host + target_type 에 매칭되는 활성 규칙을 반환합니다 (RuleResolver 핫패스).
	//
	// Deprecated: path_pattern 도입 후 후보 슬라이스를 한꺼번에 받아 application 측에서
	// 매칭하는 FindActiveCandidates 사용 권장.
	FindActive(ctx context.Context, host string, targetType model.TargetType) (*model.ParserRuleRecord, error)

	// InsertNextVersion 은 (source_name, host_pattern, path_pattern, target_type) 자연키의 다음
	// version 으로 rec 을 INSERT 합니다.
	//
	// 사용처:
	//   - Stale rule 재학습: 기존 v1 (catch-all enabled=true) 잔존 + 신규 v2 (정밀 / 갱신 selector)
	//     → Resolver 가 LENGTH(path_pattern) DESC + version DESC 로 우선순위 결정
	//   - Refiner path_pattern 정밀화: catch-all (v1, path="") + 정밀 (v2, path="/news/.*") 공존
	//     → v2 미매칭 path 는 v1 catch-all 로 fallback (silent miss 회피)
	//
	// 동작:
	//   1. 자연키의 MAX(version) 조회
	//   2. 없으면 version=1 로 INSERT, 있으면 max+1 로 INSERT
	//   3. 자연키 충돌 (race window) 시 ErrDuplicate 반환 — 호출자 retry 또는 흡수
	//
	// rec.Version 은 입력 무관 (자동 계산). 성공 시 rec.ID / Version / CreatedAt / UpdatedAt 채워짐.
	InsertNextVersion(ctx context.Context, r *model.ParserRuleRecord) error

	// HasAnyRule 은 (host_pattern, target_type) 에 대한 룰 존재 여부 + enabled 여부를 반환합니다.
	HasAnyRule(ctx context.Context, hostPattern string, targetType model.TargetType) (exists, hasEnabled bool, err error)

	// FindByNaturalKey 는 자연키로 단일 rule 을 조회합니다. enabled 필터 없음.
	FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, targetType model.TargetType, version int) (*model.ParserRuleRecord, error)

	// FindActiveCandidates 는 host + target_type 매칭 활성 rule 들을 LENGTH(path_pattern) DESC,
	// version DESC 정렬로 반환합니다.
	FindActiveCandidates(ctx context.Context, host string, targetType model.TargetType) ([]*model.ParserRuleRecord, error)

	// List 는 필터 조건에 맞는 규칙들을 반환합니다 (운영 대시보드용).
	List(ctx context.Context, filter model.ParserRuleFilter) ([]*model.ParserRuleRecord, error)

	// Delete 는 ID 로 규칙을 삭제합니다 (idempotent).
	Delete(ctx context.Context, id int64) error
}
