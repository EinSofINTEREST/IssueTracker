package publisher

import (
	"context"
	"fmt"

	"issuetracker/pkg/queue"
)

// PublishUpgrade 는 fetcher auto-upgrade (goquery → chromedp) 결정 후 실패 raw 를 재발행하는
// 메시지 배치를 Kafka 에 일괄 발행합니다 (이슈 #388).
//
// 책임 분리 (메타 #385 — fetcher 로직 강한 부분은 fetcher 잔존):
//   - **publisher 책임**: PublishBatch 호출 + 일관 로그
//   - **fetcher 책임 (Upgrader 잔존)**: 의사결정 (in-flight lock / GetByHost / Upsert /
//     Invalidate / PeekByHost), force_fetcher metadata / token 부착, retry_reason /
//     original_raw_id header 부착, stale tracking, CrawlJob 빌드
//
// host 인자는 로그 가시성 용 — 다운스트림 fetcher 가 메시지의 host 결정.
// msgs 가 빈 슬라이스면 noop (nil error).
//
// PublishBatch 실패 시 모든 ID 가 잔존하도록 caller (Upgrader) 가 RemoveByHost 호출 안 함 —
// 다음 trigger 가 자연 retry. 본 메소드는 단순 발행만 책임.
func (p *Publisher) PublishUpgrade(ctx context.Context, host string, msgs []queue.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if err := p.producer.PublishBatch(ctx, msgs); err != nil {
		return fmt.Errorf("upgrade publish batch (host=%s, count=%d): %w", host, len(msgs), err)
	}
	p.log.WithFields(map[string]interface{}{
		"host":            host,
		"republish_count": len(msgs),
	}).Info("upgrade republish published")
	return nil
}
