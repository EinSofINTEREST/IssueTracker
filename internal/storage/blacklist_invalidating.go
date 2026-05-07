package storage

import (
	"context"
	"errors"
)

// BlacklistInvalidator 는 host cache invalidate 의 최소 인터페이스입니다.
// BlacklistMatcher 가 본 인터페이스를 만족 — 구현체가 본 패키지를 import 하지 않아도 결합 가능.
type BlacklistInvalidator interface {
	Invalidate(host string)
}

// invalidatingBlacklistRepo 는 BlacklistRepository 를 wrap 하여 mutation 후 자동으로 Matcher 의
// host cache 를 invalidate 하는 decorator 입니다 (parsing_rules 의 invalidatingRepo 와 동일 패턴).
//
// 적용 정책:
//   - Insert 성공          → Invalidate (host)
//   - Insert ErrDuplicate  → Invalidate (다른 인스턴스가 INSERT 했을 가능성 — cache stale)
//   - Update 성공          → 사전 GetByID 로 host lookup 후 Invalidate
//   - Delete 성공          → 사전 GetByID 로 host lookup 후 Invalidate
type invalidatingBlacklistRepo struct {
	inner BlacklistRepository
	inv   BlacklistInvalidator
}

// WrapBlacklistWithInvalidator 는 BlacklistRepository 를 invalidatingBlacklistRepo 로 wrap 합니다.
//
// inner nil 이면 panic — wiring 버그. inv nil 이면 wrapper 만 (invalidate skip).
func WrapBlacklistWithInvalidator(inner BlacklistRepository, inv BlacklistInvalidator) BlacklistRepository {
	if inner == nil {
		panic("storage: WrapBlacklistWithInvalidator requires non-nil inner repository")
	}
	return &invalidatingBlacklistRepo{inner: inner, inv: inv}
}

func (r *invalidatingBlacklistRepo) invalidate(host string) {
	if r.inv != nil {
		r.inv.Invalidate(host)
	}
}

func (r *invalidatingBlacklistRepo) Insert(ctx context.Context, rec *BlacklistRecord) error {
	err := r.inner.Insert(ctx, rec)
	if err == nil || errors.Is(err, ErrDuplicate) {
		r.invalidate(rec.HostPattern)
	}
	return err
}

func (r *invalidatingBlacklistRepo) Update(ctx context.Context, rec *BlacklistRecord) error {
	// Update 는 host 변경 안 하므로 호출자가 rec.HostPattern 을 비워
	// 보내도 정당. 사전 GetByID 로 authoritative host 를 얻어 invalidate — Delete 와 동일 패턴.
	// pre-fetch 실패 시 fallback 으로 rec.HostPattern (비어있지 않을 때만) 사용.
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

// 이하 read-only 위임.

func (r *invalidatingBlacklistRepo) GetByID(ctx context.Context, id int64) (*BlacklistRecord, error) {
	return r.inner.GetByID(ctx, id)
}
func (r *invalidatingBlacklistRepo) FindEnabledByHost(ctx context.Context, host string) ([]*BlacklistRecord, error) {
	return r.inner.FindEnabledByHost(ctx, host)
}
func (r *invalidatingBlacklistRepo) List(ctx context.Context, f BlacklistFilter) ([]*BlacklistRecord, error) {
	return r.inner.List(ctx, f)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ BlacklistRepository = (*invalidatingBlacklistRepo)(nil)
