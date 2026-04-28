package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
)

// BacklogChecker 는 Kafka 토픽의 처리 대기 메시지(consumer-group lag) 수를 조회합니다.
//
// Backlog 는 (latest offset - committed offset) 의 모든 partition 합계를 반환합니다.
// 신규 consumer group 처럼 commit 이력이 없어 broker 가 -1 을 반환한 partition 은
// 0 으로 보정 — 미처리 메시지 전량을 lag 로 인식합니다.
type BacklogChecker interface {
	Backlog(ctx context.Context, topic string, group string) (int64, error)
}

// KafkaBacklogChecker 는 kafka-go Client 기반 BacklogChecker 구현체입니다.
//
// 단일 Client 를 재사용 — kafka-go Client 는 Transport 풀로 connection 재사용이 가능하므로
// 매 호출마다 새 Client 를 만들지 않습니다. 내부 mutable state 가 없어 동시 호출 안전.
type KafkaBacklogChecker struct {
	client *kafka.Client
}

// NewBacklogChecker 는 주어진 brokers 와 timeout 으로 새 KafkaBacklogChecker 를 생성합니다.
//
// timeout 은 개별 RPC (Metadata / ListOffsets / OffsetFetch) 의 deadline 으로 사용됩니다.
// 호출자가 ctx 에 더 짧은 deadline 을 부여한 경우 그쪽이 우선 적용됩니다.
func NewBacklogChecker(brokers []string, timeout time.Duration) *KafkaBacklogChecker {
	return &KafkaBacklogChecker{
		client: &kafka.Client{
			Addr:    kafka.TCP(brokers...),
			Timeout: timeout,
		},
	}
}

// Backlog 는 (topic, group) 의 모든 partition lag 합계를 반환합니다.
// 절차: Metadata → ListOffsets(LastOffset) → OffsetFetch → sum(latest - committed).
//
// 토픽이 존재하지 않거나 partition 수가 0 이면 0 을 반환 — Kafka 에 아직 토픽이
// 만들어지지 않은 초기 부트스트랩 단계에서도 throttle 이 publish 를 막지 않도록.
//
// committed offset 이 음수 (-1: 아직 commit 없음) 인 partition 은 lag 산정에서
// 제외 (해당 partition 의 lag = 0). 신규 consumer group 이 등록 직후 잔여 backlog
// 전체를 "미처리 메시지" 로 잘못 인식하여 publish 가 갑자기 차단되는 것을 방지 —
// 한 사이클 후 commit 이 시작되면 정상 lag 산정으로 복귀.
//
// 음수 lag (committed > latest 인 비정상 케이스) 도 0 으로 보정하여 합산 안정성 보장.
func (c *KafkaBacklogChecker) Backlog(ctx context.Context, topic, group string) (int64, error) {
	// 1. partition 목록 조회
	metaResp, err := c.client.Metadata(ctx, &kafka.MetadataRequest{
		Topics: []string{topic},
	})
	if err != nil {
		return 0, fmt.Errorf("metadata for %s: %w", topic, err)
	}

	var partitionIDs []int
	for _, t := range metaResp.Topics {
		if t.Name != topic {
			continue
		}
		if t.Error != nil {
			// 토픽 미생성/초기 부트스트랩 구간은 fail-open 으로 backlog 0 처리 —
			// throttle 이 publish 를 막지 않도록 (주석 정책과 일관). 다른 broker 에러는
			// 명시적으로 반환하여 상위 fail-open 정책이 WARN 으로 노출되도록.
			if errors.Is(t.Error, kafka.UnknownTopicOrPartition) {
				return 0, nil
			}
			return 0, fmt.Errorf("topic %s metadata error: %w", topic, t.Error)
		}
		for _, p := range t.Partitions {
			partitionIDs = append(partitionIDs, p.ID)
		}
	}
	if len(partitionIDs) == 0 {
		return 0, nil
	}

	// 2. partition 별 latest offset 조회
	offsetReqs := make([]kafka.OffsetRequest, 0, len(partitionIDs))
	for _, pid := range partitionIDs {
		offsetReqs = append(offsetReqs, kafka.LastOffsetOf(pid))
	}
	listResp, err := c.client.ListOffsets(ctx, &kafka.ListOffsetsRequest{
		Topics: map[string][]kafka.OffsetRequest{
			topic: offsetReqs,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("list offsets for %s: %w", topic, err)
	}
	latestByPartition := make(map[int]int64, len(partitionIDs))
	for _, po := range listResp.Topics[topic] {
		if po.Error != nil {
			return 0, fmt.Errorf("partition %d list offset error: %w", po.Partition, po.Error)
		}
		latestByPartition[po.Partition] = po.LastOffset
	}

	// 3. partition 별 committed offset 조회
	fetchResp, err := c.client.OffsetFetch(ctx, &kafka.OffsetFetchRequest{
		GroupID: group,
		Topics: map[string][]int{
			topic: partitionIDs,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("offset fetch for %s/%s: %w", topic, group, err)
	}
	if fetchResp.Error != nil {
		return 0, fmt.Errorf("offset fetch %s/%s: %w", topic, group, fetchResp.Error)
	}

	// 4. lag 합산
	var totalLag int64
	for _, op := range fetchResp.Topics[topic] {
		if op.Error != nil {
			return 0, fmt.Errorf("partition %d committed offset error: %w", op.Partition, op.Error)
		}
		latest, ok := latestByPartition[op.Partition]
		if !ok {
			continue
		}
		// 신규 consumer group: commit 이력 없는 partition 은 lag 산정에서 제외 —
		// 잔여 backlog 가 throttle 임계값을 초과하더라도 신규 그룹의 publish 가
		// 즉시 차단되지 않도록 (한 사이클 후 commit 시작되면 자연 복귀).
		if op.CommittedOffset < 0 {
			continue
		}
		lag := latest - op.CommittedOffset
		if lag < 0 {
			lag = 0
		}
		totalLag += lag
	}

	return totalLag, nil
}
