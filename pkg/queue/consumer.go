package queue

import (
  "context"
  "fmt"

  "github.com/segmentio/kafka-go"
)

// KafkaConsumer는 kafka-go 기반 Consumer 구현체입니다.
//
// KafkaConsumer implements the Consumer interface using kafka-go.
// It uses manual offset commit (CommitInterval: 0) to ensure at-least-once delivery.
// Always call CommitMessages after successfully processing a fetched message.
type KafkaConsumer struct {
  reader *kafka.Reader
}

// NewConsumer는 단일 토픽을 소비하는 KafkaConsumer를 생성합니다.
// CommitInterval을 0으로 설정하여 수동 commit 모드로 동작합니다.
//
// NewConsumer creates a KafkaConsumer for a single topic.
// Manual commit mode ensures messages are not lost on processing failure.
func NewConsumer(cfg Config, topic string) *KafkaConsumer {
  r := kafka.NewReader(kafka.ReaderConfig{
    Brokers:        cfg.Brokers,
    GroupID:        cfg.GroupID,
    Topic:          topic,
    MinBytes:       cfg.MinBytes,
    MaxBytes:       cfg.MaxBytes,
    CommitInterval: 0, // 수동 commit: 처리 완료 후 명시적으로 CommitMessages 호출
    StartOffset:    kafka.FirstOffset,
  })

  return &KafkaConsumer{reader: r}
}

// FetchMessage는 Kafka에서 다음 메시지를 가져옵니다.
// auto-commit하지 않으므로, 처리 완료 후 반드시 CommitMessages를 호출해야 합니다.
// context가 cancel되면 즉시 반환합니다.
func (c *KafkaConsumer) FetchMessage(ctx context.Context) (*Message, error) {
  m, err := c.reader.FetchMessage(ctx)
  if err != nil {
    return nil, fmt.Errorf("kafka fetch: %w", err)
  }

  return fromKafkaMessage(m), nil
}

// CommitMessages는 처리 완료된 메시지들의 offset을 commit합니다.
// Topic, Partition, Offset 필드만 사용하므로 Value는 비어 있어도 됩니다.
func (c *KafkaConsumer) CommitMessages(ctx context.Context, msgs ...*Message) error {
  kmsgs := make([]kafka.Message, 0, len(msgs))
  for _, msg := range msgs {
    kmsgs = append(kmsgs, kafka.Message{
      Topic:     msg.Topic,
      Partition: msg.Partition,
      Offset:    msg.Offset,
    })
  }

  if err := c.reader.CommitMessages(ctx, kmsgs...); err != nil {
    return fmt.Errorf("kafka commit: %w", err)
  }

  return nil
}

// Close는 내부 Reader를 닫습니다.
func (c *KafkaConsumer) Close() error {
  return c.reader.Close()
}

func fromKafkaMessage(m kafka.Message) *Message {
  headers := make(map[string]string, len(m.Headers))
  for _, h := range m.Headers {
    headers[h.Key] = string(h.Value)
  }

  return &Message{
    Topic:     m.Topic,
    Partition: m.Partition,
    Offset:    m.Offset,
    Key:       m.Key,
    Value:     m.Value,
    Headers:   headers,
    Time:      m.Time,
  }
}
