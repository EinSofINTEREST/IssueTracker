// service 패키지의 parser_rule.go 는 parser_rules 도메인의 비즈니스 boundary 입니다 (이슈 #431).
//
// 책임:
//   - Decorator chain 합성 (timeout + invalidating cache) — wiring 측 boilerplate 제거
//   - Repository CRUD 위임 (Insert / InsertNextVersion / Update / UpdatePathPattern /
//     FindActive / FindActiveCandidates / HasAnyRule / FindByNaturalKey / GetByID / List / Delete)
//
// 본 service 는 현 시점에선 단순 facade 역할 — cross-cutting 로직은 decorator 가 처리.
// 향후 stale 재학습 / version fallback 등 비즈니스 로직이 등장하면 본 service 에서 추가.
package service

import (
	"context"
	"time"

	"issuetracker/internal/storage/decorator"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// ParserRuleService 는 parser_rules 도메인의 비즈니스 boundary 입니다.
type ParserRuleService interface {
	Insert(ctx context.Context, r *model.ParserRuleRecord) error
	Update(ctx context.Context, r *model.ParserRuleRecord) error
	UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error
	GetByID(ctx context.Context, id int64) (*model.ParserRuleRecord, error)
	FindActive(ctx context.Context, host string, targetType model.TargetType) (*model.ParserRuleRecord, error)
	InsertNextVersion(ctx context.Context, r *model.ParserRuleRecord) error
	HasAnyRule(ctx context.Context, hostPattern string, targetType model.TargetType) (exists, hasEnabled bool, err error)
	FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, targetType model.TargetType, version int) (*model.ParserRuleRecord, error)
	FindActiveCandidates(ctx context.Context, host string, targetType model.TargetType) ([]*model.ParserRuleRecord, error)
	List(ctx context.Context, filter model.ParserRuleFilter) ([]*model.ParserRuleRecord, error)
	Delete(ctx context.Context, id int64) error
}

type parserRuleService struct {
	repo repository.ParserRuleRepository
	log  *logger.Logger
}

// ParserRuleServiceOption 은 NewParserRuleService 생성자 옵션입니다.
type ParserRuleServiceOption func(*parserRuleServiceOptions)

type parserRuleServiceOptions struct {
	timeout     time.Duration
	invalidator decorator.CacheInvalidator
}

// WithParserRuleQueryTimeout 은 repository 메서드에 query-level timeout 을 적용합니다.
func WithParserRuleQueryTimeout(d time.Duration) ParserRuleServiceOption {
	return func(o *parserRuleServiceOptions) { o.timeout = d }
}

// WithParserRuleInvalidator 는 mutation 후 자동으로 cache invalidate 를 트리거합니다.
func WithParserRuleInvalidator(inv decorator.CacheInvalidator) ParserRuleServiceOption {
	return func(o *parserRuleServiceOptions) { o.invalidator = inv }
}

// NewParserRuleService 는 ParserRuleService 인스턴스를 생성합니다 (이슈 #431).
//
// Decorator chain 자동 합성 (안쪽 → 바깥쪽):
//
//	repo → invalidator (optional) → timeout (optional) → service
func NewParserRuleService(repo repository.ParserRuleRepository, log *logger.Logger, opts ...ParserRuleServiceOption) ParserRuleService {
	o := &parserRuleServiceOptions{}
	for _, opt := range opts {
		opt(o)
	}
	wrapped := repo
	if o.invalidator != nil {
		wrapped = decorator.WrapWithInvalidator(wrapped, o.invalidator)
	}
	if o.timeout > 0 {
		wrapped = decorator.WrapParserRuleWithTimeout(wrapped, o.timeout)
	}
	return &parserRuleService{repo: wrapped, log: log}
}

// 이하 CRUD 위임.

func (s *parserRuleService) Insert(ctx context.Context, r *model.ParserRuleRecord) error {
	return s.repo.Insert(ctx, r)
}
func (s *parserRuleService) Update(ctx context.Context, r *model.ParserRuleRecord) error {
	return s.repo.Update(ctx, r)
}
func (s *parserRuleService) UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error {
	return s.repo.UpdatePathPattern(ctx, id, pattern, description)
}
func (s *parserRuleService) GetByID(ctx context.Context, id int64) (*model.ParserRuleRecord, error) {
	return s.repo.GetByID(ctx, id)
}
func (s *parserRuleService) FindActive(ctx context.Context, host string, targetType model.TargetType) (*model.ParserRuleRecord, error) {
	return s.repo.FindActive(ctx, host, targetType)
}
func (s *parserRuleService) InsertNextVersion(ctx context.Context, r *model.ParserRuleRecord) error {
	return s.repo.InsertNextVersion(ctx, r)
}
func (s *parserRuleService) HasAnyRule(ctx context.Context, hostPattern string, targetType model.TargetType) (bool, bool, error) {
	return s.repo.HasAnyRule(ctx, hostPattern, targetType)
}
func (s *parserRuleService) FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, targetType model.TargetType, version int) (*model.ParserRuleRecord, error) {
	return s.repo.FindByNaturalKey(ctx, sourceName, hostPattern, pathPattern, targetType, version)
}
func (s *parserRuleService) FindActiveCandidates(ctx context.Context, host string, targetType model.TargetType) ([]*model.ParserRuleRecord, error) {
	return s.repo.FindActiveCandidates(ctx, host, targetType)
}
func (s *parserRuleService) List(ctx context.Context, filter model.ParserRuleFilter) ([]*model.ParserRuleRecord, error) {
	return s.repo.List(ctx, filter)
}
func (s *parserRuleService) Delete(ctx context.Context, id int64) error {
	return s.repo.Delete(ctx, id)
}
