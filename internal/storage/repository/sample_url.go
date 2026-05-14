package repository

import (
	"context"

	"issuetracker/internal/storage/model"
)

// SampleURLRepository 는 parser_rule_sample_urls 테이블에 대한 데이터 접근 인터페이스입니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다.
type SampleURLRepository interface {
	// Insert 는 sample URL 을 누적합니다. 같은 (rule_id, url) 이 이미 있으면 ErrDuplicate.
	// rule_id 의 누적 수가 model.SampleCapPerRule 에 도달했으면 skip + nil (운영 cap 적용).
	Insert(ctx context.Context, ruleID int64, url string) error

	// Count 는 rule_id 의 현재 누적 sample 수를 반환합니다.
	Count(ctx context.Context, ruleID int64) (int, error)

	// List 는 rule_id 의 sample 들을 observed_at DESC 순으로 limit 만큼 반환합니다.
	List(ctx context.Context, ruleID int64, limit int) ([]*model.SampleURL, error)

	// Purge 는 rule_id 의 모든 sample 을 삭제합니다 (정밀화 완료 후 호출).
	Purge(ctx context.Context, ruleID int64) error
}
