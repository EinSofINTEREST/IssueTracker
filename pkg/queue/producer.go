package queue

import (
	"context"
	"fmt"

	"github.com/segmentio/kafka-go"
)

// KafkaProducer는 kafka-go 기반 Producer 구현체입니다.
//
// KafkaProducer implements the Producer interface using kafka-go.
// It uses key-based partitioning (Hash balancer) to preserve ordering per key.
type KafkaProducer struct {
	writer *kafka.Writer
}

// NewProducer는 새로운 KafkaProducer를 생성합니다.
// Topic을 Writer에 설정하지 않으므로, 각 Message에서 Topic을 지정해야 합니다.
//
// NewProducer creates a new KafkaProducer. Topic must be set on each Message,
// not on the writer, to support publishing to multiple topics.
func NewProducer(cfg Config) *KafkaProducer {
	w := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Balancer:     &kafka.Hash{},
		WriteTimeout: cfg.WriteTimeout,
		MaxAttempts:  cfg.MaxRetries,
		Compression:  kafka.Snappy,
		RequiredAcks: kafka.RequireOne,
		// AllowAutoTopicCreation: false (프로덕션에서는 수동으로 토픽 생성 권장)
	}

	return &KafkaProducer{writer: w}
}

// Publish는 단일 메시지를 Kafka에 발행합니다.
func (p *KafkaProducer) Publish(ctx context.Context, msg Message) error {
	km := toKafkaMessage(msg)

	if err := p.writer.WriteMessages(ctx, km); err != nil {
		return fmt.Errorf("kafka publish to %s: %w", msg.Topic, err)
	}

	return nil
}

// PublishBatch는 여러 메시지를 한 번의 요청으로 Kafka에 발행합니다.
// 개별 Publish 호출보다 처리량이 높습니다.
func (p *KafkaProducer) PublishBatch(ctx context.Context, msgs []Message) error {
	kmsgs := make([]kafka.Message, 0, len(msgs))
	for _, msg := range msgs {
		kmsgs = append(kmsgs, toKafkaMessage(msg))
	}

	if err := p.writer.WriteMessages(ctx, kmsgs...); err != nil {
		return fmt.Errorf("kafka batch publish (%d messages): %w", len(msgs), err)
	}

	return nil
}

// Close는 내부 Writer를 닫고 미전송 메시지를 flush합니다.
func (p *KafkaProducer) Close() error {
	return p.writer.Close()
}

func toKafkaMessage(msg Message) kafka.Message {
	return kafka.Message{
		Topic:   msg.Topic,
		Key:     msg.Key,
		Value:   msg.Value,
		Time:    msg.Time,
		Headers: toKafkaHeaders(msg.Headers),
	}
}

func toKafkaHeaders(headers map[string]string) []kafka.Header {
	if len(headers) == 0 {
		return nil
	}

	result := make([]kafka.Header, 0, len(headers))
	for k, v := range headers {
		result = append(result, kafka.Header{
			Key:   k,
			Value: []byte(v),
		})
	}

	return result
}
