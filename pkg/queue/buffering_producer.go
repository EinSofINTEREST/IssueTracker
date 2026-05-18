package queue

import (
	"context"
	"errors"
	"fmt"

	"issuetracker/pkg/logger"
)

// JobBuffer 는 crawl topic 메시지를 priority label 별로 임시 적재하는 buffer 인터페이스입니다
// (이슈 #510). Redis LIST 기반 FIFO 구현체가 pkg/redis 에 존재.
//
// 본 인터페이스는 BufferingProducer 와 BufferDrainer 가 공유 — Redis 직접 의존 회피.
// Noop 구현체 (NoopJobBuffer) 를 wiring 에서 사용하면 buffer 기능이 자동 비활성.
//
// EnqueueBatch (gemini PR #511 피드백): N개 payload 를 단일 Redis pipeline 으로 enqueue —
// PublishBatch / drainer 재적재 경로의 round-trip 부하 N→1 절감.
type JobBuffer interface {
	EnqueueJob(ctx context.Context, label string, payload []byte, maxLen int64) error
	EnqueueBatch(ctx context.Context, label string, payloads [][]byte, maxLen int64) error
	DrainJobs(ctx context.Context, label string, n int) ([][]byte, error)
	JobBufferLen(ctx context.Context, label string) (int64, error)
}

// NoopJobBuffer 는 모든 호출이 zero-value 를 반환하는 fail-safe 구현체입니다.
// Redis 비활성 / buffer 기능 opt-out 시 wiring 에서 사용.
type NoopJobBuffer struct{}

// EnqueueJob 항상 error 반환 — BufferingProducer 가 fallback 경로를 타도록 신호.
func (NoopJobBuffer) EnqueueJob(_ context.Context, _ string, _ []byte, _ int64) error {
	return errors.New("noop job buffer: enqueue not supported")
}

// EnqueueBatch 도 동등하게 error — fallback 신호.
func (NoopJobBuffer) EnqueueBatch(_ context.Context, _ string, _ [][]byte, _ int64) error {
	return errors.New("noop job buffer: enqueue batch not supported")
}

// DrainJobs 항상 빈 슬라이스 반환 — drainer 가 매 tick 마다 idle 로 인식.
func (NoopJobBuffer) DrainJobs(_ context.Context, _ string, _ int) ([][]byte, error) {
	return nil, nil
}

// JobBufferLen 항상 0 — 모니터링이 buffer 미사용 으로 인식.
func (NoopJobBuffer) JobBufferLen(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

// BufferingProducer 는 normal / low priority crawl topic 메시지를 Redis JobBuffer 에 임시
// 적재하는 Producer 데코레이터입니다 (이슈 #510).
//
// 동작:
//   - msg.Topic == TopicCrawlNormal → JobBuffer.EnqueueJob(label="normal", payload)
//   - msg.Topic == TopicCrawlLow    → JobBuffer.EnqueueJob(label="low", payload)
//   - 그 외 (high crawl / raw / normalized / validated / enriched / dlq / ...) → underlying.Publish
//
// Enqueue 실패 시 underlying producer 로 fallback — Redis 장애가 publish 자체를 막지 않도록
// (fail-open). 빈도가 잦으면 운영자가 WARN 로그로 감지.
//
// PublishBatch 는 message 단위로 routing — 같은 batch 안에 normal/low/high 가 섞여 있어도 정상 처리
// 단, Redis enqueue 와 Kafka publish 가 서로 다른 트랜잭션이라 batch atomicity 는 보장 X
// (기존 KafkaProducer.WriteMessages 의 multi-message atomicity 와 동일 한계).
type BufferingProducer struct {
	underlying Producer
	buffer     JobBuffer
	maxLen     int64
	log        *logger.Logger
}

// NewBufferingProducer 는 underlying Producer 를 buffer 데코레이터로 감쌉니다.
//
// 인자:
//   - underlying : 실제 Kafka publish 책임 (보통 *KafkaProducer)
//   - buffer     : Redis 또는 Noop. nil 이면 NoopJobBuffer 로 자동 대체 — 기능 비활성
//   - maxLen     : EnqueueJob 의 LIST 최대 길이 (>0 이면 LTRIM 적용)
//   - log        : enqueue 실패 / fallback 등 WARN 로그
//
// underlying / log 는 필수 (nil 이면 NewBufferingProducer 자체가 fallback 으로 underlying 만 반환할 수
// 있으나, 그러면 데코 의미 없음 → 명시적 error 또는 호출자가 wiring 책임).
func NewBufferingProducer(underlying Producer, buffer JobBuffer, maxLen int64, log *logger.Logger) *BufferingProducer {
	if buffer == nil {
		buffer = NoopJobBuffer{}
	}
	return &BufferingProducer{
		underlying: underlying,
		buffer:     buffer,
		maxLen:     maxLen,
		log:        log,
	}
}

// labelForTopic 은 crawl topic → buffer label 매핑을 반환합니다.
// normal/low 외 토픽 (high / 다른 stage 토픽) 은 빈 문자열 — 호출자가 buffer 우회 신호로 사용.
func labelForTopic(topic string) string {
	switch topic {
	case TopicCrawlNormal:
		return "normal"
	case TopicCrawlLow:
		return "low"
	default:
		return ""
	}
}

// Publish 는 msg.Topic 에 따라 buffer 또는 underlying 으로 라우팅합니다.
func (p *BufferingProducer) Publish(ctx context.Context, msg Message) error {
	label := labelForTopic(msg.Topic)
	if label == "" {
		return p.underlying.Publish(ctx, msg)
	}

	payload, err := encodeBufferedMessage(msg)
	if err != nil {
		// 직렬화 실패는 underlying 에 직접 위임해 publish 손실 회피.
		p.log.WithFields(map[string]interface{}{
			"topic": msg.Topic,
			"label": label,
		}).WithError(err).Warn("buffered message encode failed, falling back to direct publish")
		return p.underlying.Publish(ctx, msg)
	}

	if err := p.buffer.EnqueueJob(ctx, label, payload, p.maxLen); err != nil {
		// Redis 장애 등 — underlying 으로 fallback. 운영자가 WARN 누적 감지.
		p.log.WithFields(map[string]interface{}{
			"topic": msg.Topic,
			"label": label,
		}).WithError(err).Warn("buffer enqueue failed, falling back to direct publish")
		return p.underlying.Publish(ctx, msg)
	}
	return nil
}

// PublishBatch 는 메시지 단위로 routing 합니다 — 같은 batch 안 high/normal/low 혼합 가능.
//
// 구현 (gemini PR #511 피드백 — 순차 EnqueueJob 회피):
//  1. label 별 (normal/low) payload 슬라이스 + direct-target 슬라이스로 분리
//  2. 각 label 에 대해 1회 EnqueueBatch 호출 — Redis 1 RTT (multi-arg LPUSH)
//  3. label enqueue 실패 시 해당 label 의 모든 메시지를 direct 로 재분류 — Redis 장애 fallback
//  4. direct-target: underlying.PublishBatch 1회
//
// 모든 enqueue 가 성공해도 direct-target 의 PublishBatch 가 실패하면 본 batch 가 부분 성공 —
// 호출자는 본 batch 단위의 transactional 보장을 기대하지 않아야 함 (KafkaProducer 와 동일).
func (p *BufferingProducer) PublishBatch(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}

	// label 별 buffered payload + 원본 message (enqueue 실패 시 direct 로 재분류) 모음.
	type bufferedItem struct {
		msg     Message
		payload []byte
	}
	byLabel := make(map[string][]bufferedItem)
	direct := make([]Message, 0, len(msgs))

	for _, msg := range msgs {
		label := labelForTopic(msg.Topic)
		if label == "" {
			direct = append(direct, msg)
			continue
		}
		payload, err := encodeBufferedMessage(msg)
		if err != nil {
			p.log.WithFields(map[string]interface{}{
				"topic": msg.Topic,
				"label": label,
			}).WithError(err).Warn("buffered message encode failed, falling back to direct publish")
			direct = append(direct, msg)
			continue
		}
		byLabel[label] = append(byLabel[label], bufferedItem{msg: msg, payload: payload})
	}

	// label 별 1회 EnqueueBatch — Redis round-trip N→1 절감.
	for label, items := range byLabel {
		payloads := make([][]byte, len(items))
		for i, it := range items {
			payloads[i] = it.payload
		}
		if err := p.buffer.EnqueueBatch(ctx, label, payloads, p.maxLen); err != nil {
			p.log.WithFields(map[string]interface{}{
				"label": label,
				"count": len(payloads),
			}).WithError(err).Warn("buffer enqueue batch failed, falling back to direct publish")
			for _, it := range items {
				direct = append(direct, it.msg)
			}
		}
	}

	if len(direct) == 0 {
		return nil
	}
	if err := p.underlying.PublishBatch(ctx, direct); err != nil {
		return fmt.Errorf("buffering producer direct batch (%d msgs): %w", len(direct), err)
	}
	return nil
}

// Close 는 underlying.Close 를 위임 호출합니다. JobBuffer 의 lifecycle 은 외부 (Client) 가 관리.
func (p *BufferingProducer) Close() error {
	return p.underlying.Close()
}

// Underlying 은 BufferDrainer 가 drain 결과를 직접 Kafka 로 publish 할 때 사용할
// underlying Producer 접근자입니다 (data flow: Redis pop → underlying.PublishBatch).
//
// drainer 는 BufferingProducer 가 아니라 underlying 을 호출해야 함 — 그렇지 않으면 drain 한
// payload 가 다시 buffer 에 enqueue 되는 무한 루프.
func (p *BufferingProducer) Underlying() Producer {
	return p.underlying
}
