package queue

import (
	"encoding/json"
	"fmt"
	"time"
)

// bufferedMessage 는 Redis JobBuffer 에 저장되는 직렬화 가능한 Message 표현입니다 (이슈 #510).
//
// queue.Message 는 map[string]string 헤더와 []byte 필드를 포함 — encoding/json 으로 직렬화 가능.
// 그러나 호환성 / forward-compat 위해 명시적 struct 로 마샬링 — 향후 Message 에 non-serializable
// 필드 추가될 가능성 대비.
type bufferedMessage struct {
	Topic      string            `json:"topic"`
	Key        []byte            `json:"key,omitempty"`
	Value      []byte            `json:"value"`
	Time       time.Time         `json:"time,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	BufferedAt time.Time         `json:"buffered_at"`
}

// encodeBufferedMessage 는 Message 를 JSON 직렬화하여 Redis 저장용 payload 를 생성합니다.
// BufferedAt 은 drainer 가 buffer 체류 시간을 측정할 수 있도록 enqueue 시점에 기록.
func encodeBufferedMessage(msg Message) ([]byte, error) {
	bm := bufferedMessage{
		Topic:      msg.Topic,
		Key:        msg.Key,
		Value:      msg.Value,
		Time:       msg.Time,
		Headers:    msg.Headers,
		BufferedAt: time.Now(),
	}
	data, err := json.Marshal(bm)
	if err != nil {
		return nil, fmt.Errorf("encode buffered message: %w", err)
	}
	return data, nil
}

// DecodeBufferedMessage 는 Redis 에서 drain 한 payload 를 Message 로 역직렬화합니다.
// BufferDrainer 가 사용 — drainer 패키지에서 본 함수를 호출해 Kafka publish 입력 구성.
//
// 반환된 bufferedAt 은 drainer 의 buffer 체류 시간 metric / 로그 용도. msg 의 Time 과는 다름
// (Time 은 producer 가 message 생성 시점, BufferedAt 은 Redis enqueue 시점).
func DecodeBufferedMessage(payload []byte) (Message, time.Time, error) {
	var bm bufferedMessage
	if err := json.Unmarshal(payload, &bm); err != nil {
		return Message{}, time.Time{}, fmt.Errorf("decode buffered message: %w", err)
	}
	return Message{
		Topic:   bm.Topic,
		Key:     bm.Key,
		Value:   bm.Value,
		Time:    bm.Time,
		Headers: bm.Headers,
	}, bm.BufferedAt, nil
}
