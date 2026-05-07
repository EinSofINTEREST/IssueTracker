package locks

import (
	"context"
	"errors"
	"time"

	"issuetracker/internal/processor/fetcher/core"
)

// DefaultCategoryTTL 은 Category target 의 pipeline guard TTL default 입니다 (이슈 #285).
//
// Category 는 정기 갱신이 본질이므로 Article (24h) 보다 훨씬 짧은 TTL.
// fetch + ParseLinks 한 사이클이 완료될 때까지만 marker 가 유지되도록 — 운영 환경의
// SCHEDULER_JOB_TIMEOUT (default 30s) 의 2배 정도가 안전망. 명시적 Release 가 정상 흐름이고
// TTL 은 fallback (worker 가 release 호출 못 하고 죽은 경우 자동 회수).
const DefaultCategoryTTL = 60 * time.Second

// PipelineGuard 는 publish 진입점에서 \"이 URL 이 현재 파이프라인에 있는가\" 를 체크하는 통합
// 게이트입니다 (이슈 #285).
//
// 의도:
//
//	"Scheduler / Publisher / 그 외 publish 진입점이 일관된 시맨틱으로 pipeline 진입을 통제."
//
// IngestionLock 을 wrap 하여 target type 별 TTL 정책을 적용:
//   - Category: 단명 TTL (default 60s) — cycle 완료 시 release 또는 TTL fallback
//   - Article : 24h TTL (기존 IngestionLock 정책 유지)
//
// 향후 다른 target type 추가 시 본 wrapper 만 확장 — 호출자 측 코드 변경 없이.
type PipelineGuard struct {
	lock        IngestionLock
	categoryTTL time.Duration
}

// NewPipelineGuard 는 IngestionLock 위에 target type 별 TTL 정책을 부여하는 PipelineGuard 를
// 반환합니다.
//
// categoryTTL <= 0 이면 DefaultCategoryTTL 사용.
// lock 이 nil 이면 NoopIngestionLock 으로 fallback — Redis 미설정 환경에서도 panic 없이 동작.
func NewPipelineGuard(lock IngestionLock, categoryTTL time.Duration) *PipelineGuard {
	if lock == nil {
		lock = NoopIngestionLock{}
	}
	if categoryTTL <= 0 {
		categoryTTL = DefaultCategoryTTL
	}
	return &PipelineGuard{lock: lock, categoryTTL: categoryTTL}
}

// CheckAndAcquire 는 url 의 pipeline 진입 marker 를 target type 에 맞는 TTL 로 set 시도합니다.
//
// 반환값:
//   - acquired=true  : 신규 진입 — 호출자가 publish 진행
//   - acquired=false : 이미 pipeline 안 (다른 publish 가 marker 점유) — 호출자가 skip
//   - err != nil     : Redis 일시 장애 등 — 호출자 정책 (보통 fail-open) 으로 처리
//
// targetType 별 TTL (PR #286 gemini 리뷰 — 명시적 switch 로 안전성 강화):
//   - core.TargetTypeCategory : categoryTTL (단명, default 60s)
//   - core.TargetTypeArticle  : IngestionLock 의 default TTL (24h)
//   - 그 외 (Sitemap 등 미래 target type) : IngestionLock default TTL fallback
//     — 새 target type 도입 시 본 switch 에 명시적 case 추가 권장.
func (g *PipelineGuard) CheckAndAcquire(ctx context.Context, url string, targetType core.TargetType) (bool, error) {
	if g == nil || g.lock == nil {
		return false, errors.New("pipeline guard lock is nil")
	}
	switch targetType {
	case core.TargetTypeCategory:
		return g.lock.AcquireWithTTL(ctx, url, g.categoryTTL)
	case core.TargetTypeArticle:
		return g.lock.Acquire(ctx, url)
	default:
		// 알려지지 않은 target type — default TTL 적용 + Sitemap 등 추가 시 명시적 case 도입 권장.
		return g.lock.Acquire(ctx, url)
	}
}

// Release 는 url 의 pipeline marker 를 즉시 제거합니다.
//
// Category cycle 완료 시 fetcher / parser worker 가 호출 — 다음 scheduler 주기에 즉시 진입
// 가능하도록. Article 도 명시적 release 가능하나 정상 흐름은 24h TTL 만료 (운영자 강제 재크롤
// 케이스에 한함).
//
// nil receiver / nil lock 보호 — Acquire 와 동일.
func (g *PipelineGuard) Release(ctx context.Context, url string) error {
	if g == nil || g.lock == nil {
		return errors.New("pipeline guard lock is nil")
	}
	return g.lock.Invalidate(ctx, url)
}
