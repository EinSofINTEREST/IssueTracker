package primitive

import "context"

// RawIDTracker 는 host 단위로 실패한 raw_id 를 timestamp 순으로 추적합니다.
//
// 자동 chromedp 전환 trigger 가 PeekByHost 로 가장 최근 실패 raw_id 들을 가져와 새 CrawlJob
// 으로 republish. raw_contents 테이블에 host 컬럼이 없어 application 레이어에서 host → raw_id
// (recency 순) 관계를 추적.
//
// Peek-then-Remove 패턴:
//   - PeekByHost 는 최근 N 개를 조회만 — 실패 시 ID 가 손실되지 않음
//   - 호출자 (Upgrader) 가 publish 성공 후 RemoveByHost 로 삭제
//   - publish 실패 시 ID 가 잔존 → 다음 trigger 가 자연스럽게 재시도
type RawIDTracker interface {
	// Track 은 host 의 실패 raw_id 를 timestamp score 와 함께 등록합니다. ttl 만료 시 자연 정리.
	// host 또는 rawID 가 빈 문자열이면 noop.
	Track(ctx context.Context, host, rawID string) error

	// PeekByHost 는 host 의 가장 최근 실패 raw_id 를 최대 limit 개 조회합니다 (제거 안 함).
	// score DESC 순 — 가장 최근 항목이 첫 번째.
	// 호출자가 republish 성공 후 RemoveByHost 로 명시적 제거.
	PeekByHost(ctx context.Context, host string, limit int) ([]string, error)

	// RemoveByHost 는 추적기에서 지정된 raw_id 들을 제거합니다. 존재하지 않는 ID 는 무시.
	// 빈 슬라이스 / 빈 host 는 noop.
	RemoveByHost(ctx context.Context, host string, rawIDs []string) error
}

type noopRawIDTracker struct{}

// NewNoopRawIDTracker 는 모든 메소드가 noop 인 RawIDTracker 를 반환합니다.
func NewNoopRawIDTracker() RawIDTracker { return noopRawIDTracker{} }

func (noopRawIDTracker) Track(_ context.Context, _, _ string) error { return nil }
func (noopRawIDTracker) PeekByHost(_ context.Context, _ string, _ int) ([]string, error) {
	return nil, nil
}
func (noopRawIDTracker) RemoveByHost(_ context.Context, _ string, _ []string) error {
	return nil
}
