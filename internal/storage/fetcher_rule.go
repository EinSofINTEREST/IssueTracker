package storage

import (
	"context"
	"time"
)

// FetcherKind 는 fetch 단계에서 사용할 도구 식별자입니다 (이슈 #175 단계 1, sub-issue #219).
//
// FetcherKind discriminates between the static-HTML fetcher (goquery) and the headless
// browser fetcher (chromedp). host 단위로 어느 fetcher 가 우선인지 결정하는 단일 키.
type FetcherKind string

const (
	// FetcherGoQuery: 정적 HTML fetch (가벼움, 기본). lazy-load / SPA 가 아닌 일반 페이지에 적합.
	FetcherGoQuery FetcherKind = "goquery"
	// FetcherChromedp: 헤드리스 브라우저 fetch (무거움). SPA / dynamic content 사이트에 적용.
	FetcherChromedp FetcherKind = "chromedp"
)

// IsValid 는 fetcher_rules.fetcher CHECK 제약과 동일한 검증을 application 측에서 수행합니다.
func (k FetcherKind) IsValid() bool {
	return k == FetcherGoQuery || k == FetcherChromedp
}

// FetcherRuleRecord 는 fetcher_rules 테이블의 단일 행입니다.
//
// FetcherRuleRecord represents a single row of the fetcher_rules table.
type FetcherRuleRecord struct {
	ID          int64
	HostPattern string      // exact host (예: "edition.cnn.com")
	Fetcher     FetcherKind // "goquery" | "chromedp"
	Reason      string      // "manual" | "auto_upgrade_validation" | ...
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// FetcherRuleRepository 는 fetcher_rules 테이블에 대한 데이터 접근 인터페이스입니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — Resolver 가 핫패스에서 GetByHost 를 호출.
type FetcherRuleRepository interface {
	// Upsert 는 host_pattern 단위 UPSERT 입니다. 새 row 면 INSERT, 기존이면 fetcher / reason / updated_at
	// 갱신. host_pattern 의 빈 문자열은 storage.ErrInvalid 반환. fetcher 가 IsValid 가 아니면 동일.
	Upsert(ctx context.Context, host string, fetcher FetcherKind, reason string) error

	// GetByHost 는 host_pattern 으로 단일 row 를 조회합니다.
	// 매칭 없으면 storage.ErrNotFound 반환 — Resolver 가 errors.Is 로 분기.
	GetByHost(ctx context.Context, host string) (*FetcherRuleRecord, error)

	// List 는 모든 fetcher_rules 를 host_pattern ASC 로 반환합니다 (운영 도구용).
	// 본 단계에서는 페이지네이션 미도입 — host 수가 수백 단위 까지는 단일 응답 충분.
	List(ctx context.Context) ([]*FetcherRuleRecord, error)

	// Delete 는 host_pattern 으로 row 를 제거합니다. 존재하지 않아도 nil 반환 (idempotent).
	Delete(ctx context.Context, host string) error
}
