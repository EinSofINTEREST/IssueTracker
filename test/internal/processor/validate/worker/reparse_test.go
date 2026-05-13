package worker_test

// reparse_test.go — Sub A (#364) 의 단위 테스트.
//
// 검증 항목:
//  1. IsReparseEligible — VAL_001, VAL_003 만 true, 그 외 (VAL_002/004/005/006) false
//  2. readReparseCount — 헤더 부재 / 빈 / 잘못된 값 / 정상 값
//  3. validate worker process — cfg.ReparseEnabled + 적격 에러 + count<max 시
//     TopicCrawlNormal 에 reparse CrawlJob 발행 (헤더에 count+1 + reason)
//  4. validate worker — count == max 도달 시 reparse 안 하고 기존 DLQ 경로 사용
//  5. validate worker — cfg.ReparseEnabled=false 시 reparse 자체 비활성

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/validate/worker"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/queue"
)

func TestIsReparseEligible(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"VAL_001 PublishedAt required → eligible", core.NewValidationError(core.CodeValMissingField, "x", nil), true},
		{"VAL_003 Title min_length → eligible", core.NewValidationError(core.CodeValContentShort, "x", nil), true},
		{"VAL_002 format → NOT eligible", core.NewValidationError(core.CodeValInvalidFormat, "x", nil), false},
		{"VAL_004 max_length → NOT eligible", core.NewValidationError(core.CodeValContentLong, "x", nil), false},
		{"VAL_005 quality_low → NOT eligible", core.NewValidationError(core.CodeValQualityLow, "x", nil), false},
		{"VAL_006 spam → NOT eligible", core.NewValidationError(core.CodeValSpam, "x", nil), false},
		{"non-CrawlerError → NOT eligible", errors.New("plain error"), false},
		{"nil → NOT eligible", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, worker.IsReparseEligible(tt.err))
		})
	}
}

// 본 헬퍼는 외부 테스트 패키지에서 readReparseCount 가 unexported 라 직접 접근 불가.
// 따라서 validate worker process 의 동작 (publish 시 헤더 값) 으로 간접 검증.

// makeContentMsgWithReparseHeader 는 reparse count 헤더가 설정된 normalized 메시지를 생성합니다.
func makeContentMsgWithReparseHeader(t *testing.T, content *core.Content, reparseCount int) *queue.Message {
	t.Helper()
	ref := core.ContentRef{
		ID:         content.ID,
		URL:        content.URL,
		Country:    content.Country,
		SourceInfo: core.SourceInfo{Name: content.SourceID, Country: content.Country},
	}
	refBytes, _ := json.Marshal(ref)
	pm := core.ProcessingMessage{
		ID:        "job-test",
		Timestamp: time.Now(),
		Country:   content.Country,
		Stage:     "normalized",
		Data:      refBytes,
	}
	pmBytes, _ := json.Marshal(pm)

	headers := map[string]string{}
	if reparseCount > 0 {
		headers[core.HeaderValidateReparseCount] = strconv.Itoa(reparseCount)
	}
	return &queue.Message{
		Topic:   queue.TopicNormalized,
		Key:     []byte(content.URL),
		Value:   pmBytes,
		Headers: headers,
	}
}

// invalidNewsContent 는 PublishedAt 부재로 VAL_001 을 일으키는 news content 입니다.
func invalidNewsContent() *core.Content {
	return &core.Content{
		ID:    "ref-001",
		URL:   "https://news.example.com/article/123",
		Title: "Sample news article title is long enough",
		Body: "This is a sample news article body that has enough length to pass the body min_length check. " +
			"It contains multiple sentences with various words to also satisfy min_word_count. " +
			"The validator should reject only on PublishedAt required. Some more filler text here. " +
			"And yet more filler. And more. And yet more. Enough. Surely enough words now.",
		SourceID:    "naver",
		SourceType:  "news",
		Country:     "KR",
		Language:    "ko",
		PublishedAt: time.Time{}, // zero → VAL_001
	}
}

// runProcessOnce 는 ConsumerPool 의 consumer→processor→commit loop 1회를 trigger 합니다.
func runProcessOnce(t *testing.T, consumer *mockConsumer, w *worker.Worker, msg *queue.Message) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	consumer.On("FetchMessage", mock.Anything).Return(msg, nil).Once()
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)
	w.Start(ctx)
	<-ctx.Done()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = w.Stop(stopCtx)
}

// TestWorker_Reparse_FirstAttempt_PublishesCrawlJob 는 reparse-eligible 에러 + count 0 일 때
// TopicCrawlNormal 에 reparse CrawlJob 이 발행되는지 검증합니다.
func TestWorker_Reparse_FirstAttempt_PublishesCrawlJob(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)
	content := invalidNewsContent()
	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	contentSvc.On("Delete", mock.Anything, content.ID).Return(nil)

	cfg := config.DefaultValidateConfig()
	cfg.ReparseEnabled = true

	var publishedCrawlJob queue.Message
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicCrawlNormal
	})).Run(func(args mock.Arguments) {
		publishedCrawlJob = args.Get(1).(queue.Message)
	}).Return(nil)

	w := worker.NewWorker(consumer, newTestPublisher(producer), contentSvc, locks.NewNoopStageGate(), 1, cfg)
	runProcessOnce(t, consumer, w, makeContentMsgWithReparseHeader(t, content, 0))

	require.Equal(t, queue.TopicCrawlNormal, publishedCrawlJob.Topic)
	assert.Equal(t, "1", publishedCrawlJob.Headers[core.HeaderValidateReparseCount],
		"first reparse 의 count 헤더는 1 이어야 함")
	assert.NotEmpty(t, publishedCrawlJob.Headers[core.HeaderValidateReparseReason],
		"reason 헤더 비어 있으면 안 됨")
	// target_type 헤더는 fetcher chain handler 가 분기에 사용하는 wire 포맷 (core.TargetType 문자열).
	assert.Equal(t, string(core.TargetTypeArticle), publishedCrawlJob.Headers[core.HeaderTargetType])
	assert.Equal(t, content.SourceID, publishedCrawlJob.Headers[core.HeaderCrawler])
	// timeout_ms 헤더 — 원본 부재 시 reparseJobTimeoutDefault (30s = 30000ms)
	assert.Equal(t, "30000", publishedCrawlJob.Headers[core.HeaderTimeoutMs])

	var job core.CrawlJob
	require.NoError(t, json.Unmarshal(publishedCrawlJob.Value, &job))
	assert.Equal(t, content.URL, job.Target.URL)
	assert.Equal(t, content.SourceID, job.CrawlerName)
	// Key 는 job.ID 패턴 (기존 crawl 토픽 발행과 일관)
	assert.Equal(t, []byte(job.ID), publishedCrawlJob.Key)

	// content 가 삭제됐는지 확인 (reparse cycle 의 사전조건)
	contentSvc.AssertCalled(t, "Delete", mock.Anything, content.ID)
	// DLQ 발행은 일어나지 않아야 함
	for _, c := range producer.Calls {
		if c.Method == "Publish" {
			m := c.Arguments.Get(1).(queue.Message)
			assert.NotEqual(t, queue.TopicDLQ, m.Topic, "reparse 분기에서 DLQ 발행 X")
		}
	}
}

// TestWorker_Reparse_PropagatesOriginalHeaders 는 원본 메시지의 timeout_ms 등 trace 헤더가
// reparse CrawlJob 에 propagate 되는지 검증합니다 (gemini 반영).
func TestWorker_Reparse_PropagatesOriginalHeaders(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)
	content := invalidNewsContent()
	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	contentSvc.On("Delete", mock.Anything, content.ID).Return(nil)

	cfg := config.DefaultValidateConfig()
	cfg.ReparseEnabled = true

	var publishedCrawlJob queue.Message
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicCrawlNormal
	})).Run(func(args mock.Arguments) {
		publishedCrawlJob = args.Get(1).(queue.Message)
	}).Return(nil)

	msg := makeContentMsgWithReparseHeader(t, content, 0)
	msg.Headers[core.HeaderTimeoutMs] = "60000" // 60s — 원본 timeout
	msg.Headers["x-trace-id"] = "trace-abc-123"

	w := worker.NewWorker(consumer, newTestPublisher(producer), contentSvc, locks.NewNoopStageGate(), 1, cfg)
	runProcessOnce(t, consumer, w, msg)

	// 원본 timeout_ms 가 그대로 reparse 메시지에 계승됨
	assert.Equal(t, "60000", publishedCrawlJob.Headers[core.HeaderTimeoutMs])
	// 임의 trace 헤더도 보존됨 (observability)
	assert.Equal(t, "trace-abc-123", publishedCrawlJob.Headers["x-trace-id"])

	// CrawlJob.Timeout 도 60s 로 설정됨
	var job core.CrawlJob
	require.NoError(t, json.Unmarshal(publishedCrawlJob.Value, &job))
	assert.Equal(t, 60*time.Second, job.Timeout)
}

// TestWorker_Reparse_MaxCount_NoCrawlJob 은 count == max 도달 시 reparse cycle 자체가
// 일어나지 않음을 검증합니다 (기존 DLQ/requeue 경로로 폴스루).
func TestWorker_Reparse_MaxCount_NoCrawlJob(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)
	content := invalidNewsContent()
	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	contentSvc.On("UpdateValidationStatus", mock.Anything, content.ID, mock.Anything, mock.Anything).Return(nil)

	cfg := config.DefaultValidateConfig()
	cfg.ReparseEnabled = true

	crawlPublished := false
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		if m.Topic == queue.TopicCrawlNormal {
			crawlPublished = true
		}
		return true
	})).Return(nil)

	w := worker.NewWorker(consumer, newTestPublisher(producer), contentSvc, locks.NewNoopStageGate(), 1, cfg)
	runProcessOnce(t, consumer, w, makeContentMsgWithReparseHeader(t, content, core.MaxValidateReparseCount))

	assert.False(t, crawlPublished, "max 도달 시 reparse CrawlJob 발행 X (기존 DLQ/requeue 흐름으로 폴스루)")
}

// TestWorker_Reparse_Disabled_NoCrawlJob 는 cfg.ReparseEnabled=false 일 때 reparse 자체가
// 비활성되는지 검증합니다.
func TestWorker_Reparse_Disabled_NoCrawlJob(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)
	content := invalidNewsContent()
	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	contentSvc.On("UpdateValidationStatus", mock.Anything, content.ID, mock.Anything, mock.Anything).Return(nil)

	cfg := config.DefaultValidateConfig()
	cfg.ReparseEnabled = false // 비활성

	crawlPublished := false
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		if m.Topic == queue.TopicCrawlNormal {
			crawlPublished = true
		}
		return true
	})).Return(nil)

	w := worker.NewWorker(consumer, newTestPublisher(producer), contentSvc, locks.NewNoopStageGate(), 1, cfg)
	runProcessOnce(t, consumer, w, makeContentMsgWithReparseHeader(t, content, 0))

	assert.False(t, crawlPublished, "ReparseEnabled=false 시 reparse CrawlJob 발행 X")
}

// 컴파일 타임 contract 보증 — storage.ErrNotFound 사용 검증용.
var _ = storage.ErrNotFound

// service 패키지 의존성 강제 — content service mock 가 인터페이스 구현 검증.
var _ service.ContentService = (*mockContentService)(nil)
