package storage

import (
	"context"
	"time"
)

// SampleURL 은 parsing_rule_sample_urls 테이블의 단일 행입니다.
//
// SampleURL represents a single accumulated sample URL for a parsing rule —
// used by the progressive refinement workflow (path_pattern 추론).
type SampleURL struct {
	ID         int64
	RuleID     int64
	URL        string
	ObservedAt time.Time
}

// SampleCapPerRule 은 같은 rule_id 의 sample 누적 상한입니다.
//
// 단계 4-2 의 trigger 가 5개 시점에 정밀화 + purge 하면 본 cap 에 도달하지 않음.
// trigger 미동작 / LLM_ENABLED=false 등으로 정밀화가 발생하지 않을 때 DB 폭증 방어.
const SampleCapPerRule = 100

// SampleURLRepository 는 parsing_rule_sample_urls 테이블에 대한 데이터 접근 인터페이스입니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다.
type SampleURLRepository interface {
	// Insert 는 sample URL 을 누적합니다. 같은 (rule_id, url) 이 이미 있으면 ErrDuplicate.
	// rule_id 의 누적 수가 SampleCapPerRule 에 도달했으면 skip + nil (운영 cap 적용 — 정상 흐름).
	Insert(ctx context.Context, ruleID int64, url string) error

	// Count 는 rule_id 의 현재 누적 sample 수를 반환합니다.
	Count(ctx context.Context, ruleID int64) (int, error)

	// List 는 rule_id 의 sample 들을 observed_at DESC 순으로 limit 만큼 반환합니다.
	// limit <= 0 이면 default (50).
	List(ctx context.Context, ruleID int64, limit int) ([]*SampleURL, error)

	// Purge 는 rule_id 의 모든 sample 을 삭제합니다 (정밀화 완료 후 호출).
	Purge(ctx context.Context, ruleID int64) error
}
