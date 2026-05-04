package worker_test

// 이슈 #237 — LLM selector 검증 실패 시 raw content 재큐잉 검증.
//
// 검증 전략 (black-box):
//   - RequeueForLLMRetry 를 직접 호출하여 producer.Publish 호출 여부 + 메시지 내용 확인
//   - maxLLMRetries 초과 시 publish 없이 warn 로그만 (producer 호출 X)
//   - target_type / crawler 헤더가 메시지에 포함되는지 확인

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
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

func newTestRef() core.RawContentRef {
	return core.RawContentRef{
		ID:        "raw-test-001",
		URL:       "https://example.com/article/1",
		FetchedAt: time.Now(),
		SourceInfo: core.SourceInfo{
			Name:    "test-crawler",
			Country: "KR",
		},
	}
}

// TestRequeueForLLMRetry_PublishesWithIncrementedCount 는 정상 재큐 시
// LLMRetryCount 가 +1 된 RawContentRef 가 TopicFetched 에 발행되는지 검증합니다.
func TestRequeueForLLMRetry_PublishesWithIncrementedCount(t *testing.T) {
	prod := &captureProducer{}
	log := logger.New(logger.DefaultConfig())

	pw := newMinimalWorker(prod, log)
	ref := newTestRef()

	pw.RequeueForLLMRetry(context.Background(), ref, 0, storage.TargetTypePage, "test-crawler")

	require.Len(t, prod.published, 1)
	msg := prod.published[0]
	assert.Equal(t, queue.TopicFetched, msg.Topic)

	var got core.RawContentRef
	require.NoError(t, json.Unmarshal(msg.Value, &got))
	assert.Equal(t, ref.ID, got.ID)
	assert.Equal(t, ref.URL, got.URL)
	assert.Equal(t, 1, got.LLMRetryCount, "LLMRetryCount 는 기존 값 +1 이어야 함")
}

// TestRequeueForLLMRetry_PreservesHeaders 는 재큐 메시지에 target_type 과 crawler 헤더가
// 포함되는지 검증합니다 (이슈 #237 피드백 — gemini/Copilot/CodeRabbit).
func TestRequeueForLLMRetry_PreservesHeaders(t *testing.T) {
	prod := &captureProducer{}
	log := logger.New(logger.DefaultConfig())

	pw := newMinimalWorker(prod, log)
	ref := newTestRef()

	pw.RequeueForLLMRetry(context.Background(), ref, 0, storage.TargetTypeList, "naver")

	require.Len(t, prod.published, 1)
	msg := prod.published[0]
	assert.Equal(t, "list", msg.Headers["target_type"], "target_type 헤더 보존")
	assert.Equal(t, "naver", msg.Headers["crawler"], "crawler 헤더 보존")
}

// TestRequeueForLLMRetry_RespectsMaxRetries 는 maxLLMRetries 초과 시 publish 없이 포기하는지 검증합니다.
func TestRequeueForLLMRetry_RespectsMaxRetries(t *testing.T) {
	prod := &captureProducer{}
	log := logger.New(logger.DefaultConfig())

	pw := newMinimalWorker(prod, log)
	ref := newTestRef()

	// maxLLMRetries = 3, llmRetryCount = 3 이면 nextCount = 4 > 3 → 재큐 중단
	pw.RequeueForLLMRetry(context.Background(), ref, 3, storage.TargetTypePage, "test-crawler")

	assert.Empty(t, prod.published, "max retry 초과 시 publish 없어야 함")
}

// TestRequeueForLLMRetry_SecondRetry 는 두 번째 재큐 시 LLMRetryCount 가 2 인지 검증합니다.
func TestRequeueForLLMRetry_SecondRetry(t *testing.T) {
	prod := &captureProducer{}
	log := logger.New(logger.DefaultConfig())

	pw := newMinimalWorker(prod, log)
	ref := newTestRef()

	pw.RequeueForLLMRetry(context.Background(), ref, 1, storage.TargetTypePage, "test-crawler")

	require.Len(t, prod.published, 1)
	var got core.RawContentRef
	require.NoError(t, json.Unmarshal(prod.published[0].Value, &got))
	assert.Equal(t, 2, got.LLMRetryCount)
}
