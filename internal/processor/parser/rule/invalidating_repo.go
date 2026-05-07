package rule

import (
	"context"
	"errors"

	"issuetracker/internal/storage"
)

// CacheInvalidator 는 (host, target_type) 튜플의 cache entry 를 무효화하는 최소 인터페이스입니다 (이슈 #288).
//
// Resolver 가 본 인터페이스를 만족 — invalidatingRepo 가 internal/storage 를 거꾸로 import 하지 않도록
// 별도 정의. 향후 다른 cache 보유 컴포넌트도 본 인터페이스만 구현하면 decorator 와 결합 가능.
type CacheInvalidator interface {
	Invalidate(host string, targetType storage.TargetType)
}

// invalidatingRepo 는 ParsingRuleRepository 를 wrap 하여 mutation 메소드 호출 후 자동으로
// CacheInvalidator.Invalidate 를 호출하는 decorator 입니다 (이슈 #288).
//
// 의도:
//
//	"mutation→invalidate 결합을 단일 책임 지점에 모아 — 호출처가 까먹어도 cache 누락 0."
//
// 적용 정책:
//   - Insert 성공          → Invalidate (host, target_type)
//   - Insert ErrDuplicate  → Invalidate (row 가 DB 에 이미 존재 — cache 불일치 가능성)
//   - Update 성공          → Invalidate (host, target_type)
//   - UpdatePathPattern 성공 → Invalidate (record 의 host/type 사전 lookup 필요 — 후술)
//   - Delete 성공          → ID 만 알 수 있어 host/type 미상 — 호출자가 명시적 Invalidate 책임 (또는 InvalidateAll 권장)
//
// UpdatePathPattern 의 호스트 정보 부재:
//
//	본 메소드는 ID 인자만 받음 — host/type 을 모름. 추가 GetByID round-trip 으로 사전 조회 후 invalidate.
//	refiner 는 호출 빈도 낮으므로 (default 5분 주기) 추가 read 부담 무시 수준.
//
// CacheInvalidator 가 nil 이면 noop wrapper — invalidate 만 skip 하고 inner 호출은 그대로 진행.
type invalidatingRepo struct {
	inner storage.ParsingRuleRepository
	inv   CacheInvalidator
}

// WrapWithInvalidator 는 ParsingRuleRepository 를 invalidatingRepo 로 wrap 합니다 (이슈 #288).
//
// 사용 예 (cmd/issuetracker/main.go):
//
//	parsingRuleRepo := pgstore.NewParsingRuleRepository(pool, log)
//	parsingRuleRepo = rule.WrapWithInvalidator(parsingRuleRepo, ruleResolver)
//
// inner 가 nil 이면 panic — wiring 버그 (호출자가 nil 검사 필요).
// inv 가 nil 이면 wrapper 역할만 (invalidate skip, 향후 wiring 단계에서 쉽게 토글).
func WrapWithInvalidator(inner storage.ParsingRuleRepository, inv CacheInvalidator) storage.ParsingRuleRepository {
	if inner == nil {
		panic("rule: WrapWithInvalidator requires non-nil inner repository")
	}
	return &invalidatingRepo{inner: inner, inv: inv}
}

func (r *invalidatingRepo) invalidate(host string, t storage.TargetType) {
	if r.inv != nil {
		r.inv.Invalidate(host, t)
	}
}

// Insert wraps inner.Insert + invalidates on success or ErrDuplicate.
//
// ErrDuplicate 시에도 invalidate — INSERT 실패했지만 동일 자연키 row 가 이미 DB 에 존재하므로
// cache 가 stale 일 가능성 (다른 인스턴스가 INSERT 했거나 운영자 manual). 이슈 #274 의 사후
// invalidate 로직을 본 decorator 로 통합.
func (r *invalidatingRepo) Insert(ctx context.Context, rec *storage.ParsingRuleRecord) error {
	err := r.inner.Insert(ctx, rec)
	if err == nil || errors.Is(err, storage.ErrDuplicate) {
		r.invalidate(rec.HostPattern, rec.TargetType)
	}
	return err
}

// Update wraps inner.Update + invalidates on success.
func (r *invalidatingRepo) Update(ctx context.Context, rec *storage.ParsingRuleRecord) error {
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

// Delete wraps inner.Delete. ID 만 받으므로 host/type 미상 — 호출자가 사전 GetByID 로 lookup
// 하여 결과를 토대로 명시 Invalidate 호출 책임. 본 decorator 는 GetByID round-trip 을 강제하지
// 않음 (Delete 가 정상 흐름에선 거의 호출되지 않으며, 운영자 admin 도구가 호출할 가능성 큼).
func (r *invalidatingRepo) Delete(ctx context.Context, id int64) error {
	return r.inner.Delete(ctx, id)
}

// 이하 read-only / find 메소드는 단순 위임 — invalidate 트리거 없음.

func (r *invalidatingRepo) GetByID(ctx context.Context, id int64) (*storage.ParsingRuleRecord, error) {
	return r.inner.GetByID(ctx, id)
}

func (r *invalidatingRepo) FindActive(ctx context.Context, host string, t storage.TargetType) (*storage.ParsingRuleRecord, error) {
	return r.inner.FindActive(ctx, host, t)
}

func (r *invalidatingRepo) FindActiveCandidates(ctx context.Context, host string, t storage.TargetType) ([]*storage.ParsingRuleRecord, error) {
	return r.inner.FindActiveCandidates(ctx, host, t)
}

func (r *invalidatingRepo) HasAnyRule(ctx context.Context, host string, t storage.TargetType) (bool, bool, error) {
	return r.inner.HasAnyRule(ctx, host, t)
}

func (r *invalidatingRepo) FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, t storage.TargetType, version int) (*storage.ParsingRuleRecord, error) {
	return r.inner.FindByNaturalKey(ctx, sourceName, hostPattern, pathPattern, t, version)
}

func (r *invalidatingRepo) List(ctx context.Context, f storage.ParsingRuleFilter) ([]*storage.ParsingRuleRecord, error) {
	return r.inner.List(ctx, f)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ storage.ParsingRuleRepository = (*invalidatingRepo)(nil)
