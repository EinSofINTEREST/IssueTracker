package worker_test

// 이슈 #237 — LLM selector 검증 실패 시 raw content 재큐잉 검증.
//
// 검증 전략 (black-box):
//   - RequeueForLLMRetry 를 직접 호출하여 producer.Publish 호출 여부 + 메시지 내용 확인
//   - maxLLMRetries 초과 시 publish 없이 warn 로그만 (producer 호출 X)

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// captureProducer 는 Publish 호출을 캡쳐하는 stub Producer 입니다.
type captureProducer struct {
	published []queue.Message
}

func (p *captureProducer) Publish(_ context.Context, msg queue.Message) error {
	p.published = append(p.published, msg)
	return nil
}
func (p *captureProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	p.published = append(p.published, msgs...)
	return nil
}
func (p *captureProducer) Close() error { return nil }

func newTestRaw() *core.RawContent {
	return &core.RawContent{
		ID:        "raw-test-001",
		URL:       "https://example.com/article/1",
		FetchedAt: time.Now(),
		SourceInfo: core.SourceInfo{
			Name:    "test-crawler",
			Country: "KR",
		},
		HTML: "<html><body>test</body></html>",
	}
}

// buildMinimalWorker 는 RequeueForLLMRetry 테스트에 필요한 최소 ParserWorker 를 만들기 위해
// 공개 생성자를 우회하지 않고, 메서드 자체가 공개(public) 이므로 직접 호출 가능합니다.
// — 단, ParserWorker 는 내부 필드라 직접 생성 불가 → 공개 생성자 사용하되 nil 허용 필드 최대 활용.

// TestRequeueForLLMRetry_PublishesWithIncrementedCount 는 정상 재큐 시
// LLMRetryCount 가 +1 된 RawContentRef 가 TopicFetched 에 발행되는지 검증합니다.
func TestRequeueForLLMRetry_PublishesWithIncrementedCount(t *testing.T) {
	prod := &captureProducer{}
	log := logger.New(logger.DefaultConfig())

	pw := newMinimalWorker(prod, log)
	raw := newTestRaw()

	pw.RequeueForLLMRetry(context.Background(), raw, 0)

	require.Len(t, prod.published, 1)
	msg := prod.published[0]
	assert.Equal(t, queue.TopicFetched, msg.Topic)

	var ref core.RawContentRef
	require.NoError(t, json.Unmarshal(msg.Value, &ref))
	assert.Equal(t, raw.ID, ref.ID)
	assert.Equal(t, raw.URL, ref.URL)
	assert.Equal(t, 1, ref.LLMRetryCount, "LLMRetryCount 는 기존 값 +1 이어야 함")
}

// TestRequeueForLLMRetry_RespectsMaxRetries 는 maxLLMRetries 초과 시 publish 없이 포기하는지 검증합니다.
func TestRequeueForLLMRetry_RespectsMaxRetries(t *testing.T) {
	prod := &captureProducer{}
	log := logger.New(logger.DefaultConfig())

	pw := newMinimalWorker(prod, log)
	raw := newTestRaw()

	// maxLLMRetries = 3, llmRetryCount = 3 이면 nextCount = 4 > 3 → 재큐 중단
	pw.RequeueForLLMRetry(context.Background(), raw, 3)

	assert.Empty(t, prod.published, "max retry 초과 시 publish 없어야 함")
}

// TestRequeueForLLMRetry_SecondRetry 는 두 번째 재큐 시 LLMRetryCount 가 2 인지 검증합니다.
func TestRequeueForLLMRetry_SecondRetry(t *testing.T) {
	prod := &captureProducer{}
	log := logger.New(logger.DefaultConfig())

	pw := newMinimalWorker(prod, log)
	raw := newTestRaw()

	pw.RequeueForLLMRetry(context.Background(), raw, 1)

	require.Len(t, prod.published, 1)
	var ref core.RawContentRef
	require.NoError(t, json.Unmarshal(prod.published[0].Value, &ref))
	assert.Equal(t, 2, ref.LLMRetryCount)
}
