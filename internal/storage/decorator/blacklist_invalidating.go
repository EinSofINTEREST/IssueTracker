// Package decorator 는 storage 계층의 cross-cutting decorator 들 (cache invalidate, query timeout)
// 을 모아놓은 sub-package 입니다.
//
// 각 decorator 는 repository 인터페이스를 받아 동일 인터페이스를 반환하므로 wiring 단계에서
// chain 으로 합성 가능합니다 (예: invalidating + timeout 합성).
package decorator

import (
	"context"
	"errors"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
)

// BlacklistInvalidator 는 host cache invalidate 의 최소 인터페이스입니다.
// BlacklistMatcher 가 본 인터페이스를 만족 — 구현체가 본 패키지를 import 하지 않아도 결합 가능.
type BlacklistInvalidator interface {
	Invalidate(host string)
}

// invalidatingBlacklistRepo 는 BlacklistRepository 를 wrap 하여 mutation 후 자동으로 Matcher 의
// host cache 를 invalidate 하는 decorator 입니다.
//
// 적용 정책:
//   - Insert 성공          → Invalidate (host)
//   - Insert ErrDuplicate  → Invalidate (다른 인스턴스가 INSERT 했을 가능성 — cache stale)
//   - Update 성공          → 사전 GetByID 로 host lookup 후 Invalidate
//   - Delete 성공          → 사전 GetByID 로 host lookup 후 Invalidate
type invalidatingBlacklistRepo struct {
	inner repository.BlacklistRepository
	inv   BlacklistInvalidator
}

// WrapBlacklistWithInvalidator 는 BlacklistRepository 를 invalidatingBlacklistRepo 로 wrap 합니다.
//
// inner nil 이면 panic — wiring 버그. inv nil 이면 wrapper 만 (invalidate skip).
func WrapBlacklistWithInvalidator(inner repository.BlacklistRepository, inv BlacklistInvalidator) repository.BlacklistRepository {
	if inner == nil {
		panic("decorator: WrapBlacklistWithInvalidator requires non-nil inner repository")
	}
	return &invalidatingBlacklistRepo{inner: inner, inv: inv}
}

func (r *invalidatingBlacklistRepo) invalidate(host string) {
	if r.inv != nil {
		r.inv.Invalidate(host)
	}
}

func (r *invalidatingBlacklistRepo) Insert(ctx context.Context, rec *model.BlacklistRecord) error {
	err := r.inner.Insert(ctx, rec)
	if err == nil || errors.Is(err, storage.ErrDuplicate) {
		r.invalidate(rec.HostPattern)
	}
	return err
}

func (r *invalidatingBlacklistRepo) Update(ctx context.Context, rec *model.BlacklistRecord) error {
	before, lookupErr := r.inner.GetByID(ctx, rec.ID)
	if err := r.inner.Update(ctx, rec); err != nil {
		return err
	}
	if lookupErr == nil && before != nil {
		r.invalidate(before.HostPattern)
		return nil
	}
	if rec.HostPattern != "" {
		r.invalidate(rec.HostPattern)
	}
	return nil
}

func (r *invalidatingBlacklistRepo) Delete(ctx context.Context, id int64) error {
	rec, lookupErr := r.inner.GetByID(ctx, id)
	if err := r.inner.Delete(ctx, id); err != nil {
		return err
	}
	if lookupErr == nil && rec != nil {
		r.invalidate(rec.HostPattern)
	}
	return nil
}

func (r *invalidatingBlacklistRepo) GetByID(ctx context.Context, id int64) (*model.BlacklistRecord, error) {
	return r.inner.GetByID(ctx, id)
}
func (r *invalidatingBlacklistRepo) FindEnabledByHost(ctx context.Context, host string) ([]*model.BlacklistRecord, error) {
	return r.inner.FindEnabledByHost(ctx, host)
}
func (r *invalidatingBlacklistRepo) List(ctx context.Context, f model.BlacklistFilter) ([]*model.BlacklistRecord, error) {
	return r.inner.List(ctx, f)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ repository.BlacklistRepository = (*invalidatingBlacklistRepo)(nil)
