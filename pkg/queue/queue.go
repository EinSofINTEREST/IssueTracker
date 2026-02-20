// Package queue provides interfaces and implementations for
// message queue operations using Apache Kafka.
//
// queue 패키지는 Apache Kafka를 사용한 메시지 큐 연산을 위한
// 인터페이스와 구현체를 제공합니다.
//
// 모든 구현체는 Producer, Consumer 인터페이스를 만족해야 합니다.
package queue

import (
  "context"
  "time"
)

// Producer는 Kafka에 메시지를 publish하는 인터페이스입니다.
//
// Producer publishes messages to Kafka topics.
// It is safe for concurrent use by multiple goroutines.
type Producer interface {
  Publish(ctx context.Context, msg Message) error
  PublishBatch(ctx context.Context, msgs []Message) error
  Close() error
}

// Consumer는 Kafka에서 메시지를 consume하는 인터페이스입니다.
//
// Consumer reads messages from Kafka topics.
// FetchMessage does NOT auto-commit; call CommitMessages after successful processing.
type Consumer interface {
  FetchMessage(ctx context.Context) (*Message, error)
  CommitMessages(ctx context.Context, msgs ...*Message) error
  Close() error
}

// Message는 Kafka 메시지를 나타냅니다.
//
// Message represents a single Kafka message with routing and payload information.
type Message struct {
  Topic     string
  Partition int
  Offset    int64
  Key       []byte
  Value     []byte
  Headers   map[string]string
  Time      time.Time
}
