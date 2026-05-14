package decorator

import (
	"context"
	"errors"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
)

// CacheInvalidator 는 (host, target_type) 튜플의 cache entry 를 무효화하는 최소 인터페이스입니다.
//
// Resolver 가 본 인터페이스를 만족 — 구현체가 본 패키지를 import 하지 않아도 결합 가능하도록
// 인터페이스 surface 를 storage 측에서 선언.
type CacheInvalidator interface {
	Invalidate(host string, targetType model.TargetType)
}

// invalidatingRepo 는 ParserRuleRepository 를 wrap 하여 mutation 메소드 호출 후 자동으로
// CacheInvalidator.Invalidate 를 호출하는 decorator 입니다.
//
// 적용 정책:
//   - Insert / InsertNextVersion 성공 / ErrDuplicate → Invalidate
//   - Update 성공                                    → Invalidate
//   - UpdatePathPattern / Delete 성공                → 사전 GetByID 로 host/type lookup 후 Invalidate
type invalidatingRepo struct {
	inner repository.ParserRuleRepository
	inv   CacheInvalidator
}

// WrapWithInvalidator 는 ParserRuleRepository 를 invalidatingRepo 로 wrap 합니다.
//
// inner 가 nil 이면 panic (wiring 버그). inv 가 nil 이면 wrapper 역할만 (invalidate skip).
func WrapWithInvalidator(inner repository.ParserRuleRepository, inv CacheInvalidator) repository.ParserRuleRepository {
	if inner == nil {
		panic("decorator: WrapWithInvalidator requires non-nil inner repository")
	}
	return &invalidatingRepo{inner: inner, inv: inv}
}

func (r *invalidatingRepo) invalidate(host string, t model.TargetType) {
	if r.inv != nil {
		r.inv.Invalidate(host, t)
	}
}

func (r *invalidatingRepo) InsertNextVersion(ctx context.Context, rec *model.ParserRuleRecord) error {
	err := r.inner.InsertNextVersion(ctx, rec)
	if err == nil || errors.Is(err, storage.ErrDuplicate) {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return err
}

func (r *invalidatingRepo) Insert(ctx context.Context, rec *model.ParserRuleRecord) error {
	err := r.inner.Insert(ctx, rec)
	if err == nil || errors.Is(err, storage.ErrDuplicate) {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return err
}

func (r *invalidatingRepo) Update(ctx context.Context, rec *model.ParserRuleRecord) error {
	err := r.inner.Update(ctx, rec)
	if err == nil {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return err
}

func (r *invalidatingRepo) UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error {
	rec, lookupErr := r.inner.GetByID(ctx, id)
	if err := r.inner.UpdatePathPattern(ctx, id, pattern, description); err != nil {
		return err
	}
	if lookupErr == nil && rec != nil {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return nil
}

func (r *invalidatingRepo) Delete(ctx context.Context, id int64) error {
	rec, lookupErr := r.inner.GetByID(ctx, id)
	if err := r.inner.Delete(ctx, id); err != nil {
		return err
	}
	if lookupErr == nil && rec != nil {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return nil
}

// 이하 read-only / find 메소드는 단순 위임 — invalidate 트리거 없음.

func (r *invalidatingRepo) GetByID(ctx context.Context, id int64) (*model.ParserRuleRecord, error) {
	return r.inner.GetByID(ctx, id)
}

func (r *invalidatingRepo) FindActive(ctx context.Context, host string, t model.TargetType) (*model.ParserRuleRecord, error) {
	return r.inner.FindActive(ctx, host, t)
}

func (r *invalidatingRepo) FindActiveCandidates(ctx context.Context, host string, t model.TargetType) ([]*model.ParserRuleRecord, error) {
	return r.inner.FindActiveCandidates(ctx, host, t)
}

func (r *invalidatingRepo) HasAnyRule(ctx context.Context, host string, t model.TargetType) (bool, bool, error) {
	return r.inner.HasAnyRule(ctx, host, t)
}

func (r *invalidatingRepo) FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, t model.TargetType, version int) (*model.ParserRuleRecord, error) {
	return r.inner.FindByNaturalKey(ctx, sourceName, hostPattern, pathPattern, t, version)
}

func (r *invalidatingRepo) List(ctx context.Context, f model.ParserRuleFilter) ([]*model.ParserRuleRecord, error) {
	return r.inner.List(ctx, f)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ repository.ParserRuleRepository = (*invalidatingRepo)(nil)
