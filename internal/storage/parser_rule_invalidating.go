package storage

import (
	"context"
	"errors"
)

// CacheInvalidator 는 (host, target_type) 튜플의 cache entry 를 무효화하는 최소 인터페이스입니다.
//
// Resolver 가 본 인터페이스를 만족 — 구현체가 본 패키지를 import 하지 않아도 결합 가능하도록
// 인터페이스 surface 를 storage 측에서 선언. 향후 다른 cache 보유 컴포넌트도 본 인터페이스만
// 구현하면 decorator 와 결합 가능.
type CacheInvalidator interface {
	Invalidate(host string, targetType TargetType)
}

// invalidatingRepo 는 ParserRuleRepository 를 wrap 하여 mutation 메소드 호출 후 자동으로
// CacheInvalidator.Invalidate 를 호출하는 decorator 입니다.
//
// 의도:
//
//	"mutation→invalidate 결합을 단일 책임 지점에 모아 — 호출처가 까먹어도 cache 누락 0."
//
// 적용 정책:
//   - Insert 성공          → Invalidate (host, target_type)
//   - Insert ErrDuplicate  → Invalidate (row 가 DB 에 이미 존재 — cache 불일치 가능성)
//   - Update 성공          → Invalidate (host, target_type)
//   - UpdatePathPattern 성공 → 사전 GetByID 로 host/type lookup 후 Invalidate
//   - Delete 성공          → 사전 GetByID 로 host/type lookup 후 Invalidate
//
// UpdatePathPattern / Delete 의 호스트 정보 부재:
//
//	두 메소드는 ID 인자만 받음 — host/type 을 모름. 추가 GetByID round-trip 으로 사전 조회 후 invalidate.
//	refiner / admin 도구는 호출 빈도 낮으므로 추가 read 부담 무시 수준.
//
// CacheInvalidator 가 nil 이면 noop wrapper — invalidate 만 skip 하고 inner 호출은 그대로 진행.
type invalidatingRepo struct {
	inner ParserRuleRepository
	inv   CacheInvalidator
}

// WrapWithInvalidator 는 ParserRuleRepository 를 invalidatingRepo 로 wrap 합니다.
//
// 사용 예 (cmd/issuetracker/main.go):
//
//	parserRuleRepo := pgstore.NewParserRuleRepository(pool, log)
//	parserRuleRepo = storage.WrapWithInvalidator(parserRuleRepo, ruleResolver)
//
// inner 가 nil 이면 panic — wiring 버그 (호출자가 nil 검사 필요).
// inv 가 nil 이면 wrapper 역할만 (invalidate skip, 향후 wiring 단계에서 쉽게 토글).
func WrapWithInvalidator(inner ParserRuleRepository, inv CacheInvalidator) ParserRuleRepository {
	if inner == nil {
		panic("storage: WrapWithInvalidator requires non-nil inner repository")
	}
	return &invalidatingRepo{inner: inner, inv: inv}
}

func (r *invalidatingRepo) invalidate(host string, t TargetType) {
	if r.inv != nil {
		r.inv.Invalidate(host, t)
	}
}

// InsertNextVersion wraps inner.InsertNextVersion + invalidates on success or ErrDuplicate.
//
// 같은 (host, target_type) 에 대한 재학습 시 cache 가 stale 일 수 있으므로 invalidate 보장.
func (r *invalidatingRepo) InsertNextVersion(ctx context.Context, rec *ParserRuleRecord) error {
	err := r.inner.InsertNextVersion(ctx, rec)
	if err == nil || errors.Is(err, ErrDuplicate) {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return err
}

// Insert wraps inner.Insert + invalidates on success or ErrDuplicate.
//
// ErrDuplicate 시에도 invalidate — INSERT 실패했지만 동일 자연키 row 가 이미 DB 에 존재하므로
// cache 가 stale 일 가능성 (다른 인스턴스가 INSERT 했거나 운영자 manual).
func (r *invalidatingRepo) Insert(ctx context.Context, rec *ParserRuleRecord) error {
	err := r.inner.Insert(ctx, rec)
	if err == nil || errors.Is(err, ErrDuplicate) {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return err
}

// Update wraps inner.Update + invalidates on success.
func (r *invalidatingRepo) Update(ctx context.Context, rec *ParserRuleRecord) error {
	err := r.inner.Update(ctx, rec)
	if err == nil {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return err
}

// UpdatePathPattern wraps inner.UpdatePathPattern + invalidates on success.
//
// 본 메소드는 host/type 을 직접 받지 않으므로 사전 GetByID 로 lookup 후 invalidate.
// inner.UpdatePathPattern 이 ErrNotFound 반환하면 lookup 결과 무관하게 단순 전파.
func (r *invalidatingRepo) UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error {
	rec, lookupErr := r.inner.GetByID(ctx, id)
	if err := r.inner.UpdatePathPattern(ctx, id, pattern, description); err != nil {
		return err
	}
	if lookupErr == nil && rec != nil {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	// lookupErr != nil — pre-fetch 실패 시 invalidate skip. caller (refiner) 가 명시 호출
	// 하지 않으면 cache stale 가능성 — 운영 환경에서는 GetByID 가 ErrNotFound 외 실패할 일은
	// DB 장애뿐이며, 그 경우 본 mutation 자체도 동일 사유로 실패할 가능성 높음 (TTL fallback).
	return nil
}

// Delete wraps inner.Delete + 사전 GetByID 로 host/type 조회 후 invalidate.
//
// UpdatePathPattern 과 동일 패턴 — decorator 의 일관성 보장 (mutation→invalidate 결합 누락 0).
// pre-fetch 실패 시 invalidate skip — TTL fallback 으로 자연 회수.
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

func (r *invalidatingRepo) GetByID(ctx context.Context, id int64) (*ParserRuleRecord, error) {
	return r.inner.GetByID(ctx, id)
}

func (r *invalidatingRepo) FindActive(ctx context.Context, host string, t TargetType) (*ParserRuleRecord, error) {
	return r.inner.FindActive(ctx, host, t)
}

func (r *invalidatingRepo) FindActiveCandidates(ctx context.Context, host string, t TargetType) ([]*ParserRuleRecord, error) {
	return r.inner.FindActiveCandidates(ctx, host, t)
}

func (r *invalidatingRepo) HasAnyRule(ctx context.Context, host string, t TargetType) (bool, bool, error) {
	return r.inner.HasAnyRule(ctx, host, t)
}

func (r *invalidatingRepo) FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, t TargetType, version int) (*ParserRuleRecord, error) {
	return r.inner.FindByNaturalKey(ctx, sourceName, hostPattern, pathPattern, t, version)
}

func (r *invalidatingRepo) List(ctx context.Context, f ParserRuleFilter) ([]*ParserRuleRecord, error) {
	return r.inner.List(ctx, f)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ ParserRuleRepository = (*invalidatingRepo)(nil)
