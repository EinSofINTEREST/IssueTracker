package worker_test

// 이슈 #502 — validation-fail 로그 필드 검증.
//
// 3개 emit 지점 (deleting-to-dlq / requeueing / triggering parser reparse) 의 로그 출력에서:
//   - `error` 필드가 부착되지 않음 (.WithError 제거)
//   - `url` 필드 존재 (사후 추적 가능)
//   - `reject_reason` 필드 존재 (rejected 원인 평탄화)
// 를 검증합니다.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor/validate/worker"
	"issuetracker/internal/storage/service"
	processorcfg "issuetracker/pkg/config/processor"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// captureLog 는 worker 가 emit 한 로그를 buffer 로 캡처하기 위한 logger 와 그 출력 버퍼를 반환합니다.
func captureLog() (*logger.Logger, *bytes.Buffer) {
	buf := new(bytes.Buffer)
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	return logger.New(cfg), buf
}

// runWorkerWithLogger 는 runWorker 와 동일하나 ctx 에 사용자 지정 logger 를 주입합니다.
// handleMessage 내부의 logger.FromContext(ctx) 가 본 logger 를 반환 — emit 캡처 가능.
func runWorkerWithLogger(t *testing.T, consumer *mockConsumer, w *worker.Worker, msg *queue.Message, log *logger.Logger) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	ctx = log.ToContext(ctx)

	consumer.On("FetchMessage", mock.Anything).Return(msg, nil).Once()
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)

	w.Start(ctx)
	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = w.Stop(stopCtx)
}

// findLogEntry 는 buf 출력에서 message 가 일치하는 첫 JSON 라인을 반환합니다.
func findLogEntry(t *testing.T, buf *bytes.Buffer, message string) map[string]interface{} {
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if msg, ok := entry["message"].(string); ok && msg == message {
			return entry
		}
	}
	t.Fatalf("expected log message %q not found in output:\n%s", message, buf.String())
	return nil
}

// newWorkerWithLogger 는 newWorker 와 동일 fixture 이지만 publisher 가 본 테스트의 logger 를 공유하도록
// 구성합니다 (publisher 도 로그를 찍기에 본 logger 로 통일하면 검증 노이즈가 줄어듦).
//
// ReparseEnabled=false 명시 — VAL_003 (Title/Body min_length) 는 IsReparseEligible 이라
// default 가 향후 true 로 변경되면 본 테스트의 의도된 DLQ/requeue 분기 대신 reparse 분기가 발화 (gemini PR #513 피드백).
// 본 테스트는 reparse 분기 외의 두 분기 (DLQ / requeue) 의 로그 출력 검증이 목적이므로 명시 잠금.
func newWorkerWithLogger(consumer queue.Consumer, producer queue.Producer, contentSvc service.ContentService, log *logger.Logger) *worker.Worker {
	pub := bus.New(producer, nil, log)
	cfg := processorcfg.DefaultValidateConfig()
	cfg.ReparseEnabled = false
	return worker.NewWorker(consumer, pub, contentSvc, locks.NewNoopStageGate(), 1, cfg)
}

// ─────────────────────────────────────────────────────────────────────────────
// 1) "content validation failed, deleting content and sending to dlq" 지점
// ─────────────────────────────────────────────────────────────────────────────

func TestValidateWorker_DLQLog_NoErrorField_HasURLAndRejectReason(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := newMockContentService()
	log, buf := captureLog()
	w := newWorkerWithLogger(consumer, producer, contentSvc, log)

	content := newNewsContent()
	content.Title = "x"         // VAL_003 min_length
	content.Body = "short body" // VAL_003 min_length
	content.PublishedAt = time.Time{}
	msg := makeProcessingMessage(content, 3) // max retries 도달 → DLQ 분기

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	contentSvc.On("UpdateValidationStatus", mock.Anything, content.ID, "rejected", mock.Anything, mock.Anything).Return(nil).Maybe()
	contentSvc.On("Delete", mock.Anything, content.ID).Return(nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorkerWithLogger(t, consumer, w, msg, log)

	entry := findLogEntry(t, buf, "content validation failed, deleting content and sending to dlq")

	// 1. .WithError 제거 — error 필드 부재
	_, hasErr := entry["error"]
	assert.False(t, hasErr, "error 필드가 부착되지 않아야 함 (.WithError 제거 검증)")

	// 2. url 필드 존재
	url, _ := entry["url"].(string)
	assert.Equal(t, content.URL, url, "url 필드는 content.URL 과 일치")

	// 3. reject_reason 평탄화
	reason, _ := entry["reject_reason"].(string)
	require.NotEmpty(t, reason, "reject_reason 필드는 validator 의 err.Error() 문자열")
	assert.Contains(t, reason, "VAL_", "reject_reason 안에 validation 에러 코드 포함")

	// 4. level=info 확인
	assert.Equal(t, "info", entry["level"], "여전히 INFO level 유지")
}

// ─────────────────────────────────────────────────────────────────────────────
// 2) "content validation failed, requeueing" 지점
// ─────────────────────────────────────────────────────────────────────────────

func TestValidateWorker_RequeueLog_NoErrorField_HasURLAndRejectReason(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := newMockContentService()
	log, buf := captureLog()
	w := newWorkerWithLogger(consumer, producer, contentSvc, log)

	content := newNewsContent()
	content.Title = "x"
	content.Body = "short body"
	content.PublishedAt = time.Time{}
	// retry count=0 → max(3) 미도달 → requeue 분기
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorkerWithLogger(t, consumer, w, msg, log)

	entry := findLogEntry(t, buf, "content validation failed, requeueing")

	_, hasErr := entry["error"]
	assert.False(t, hasErr, "requeue 분기도 error 필드 부재 (.WithError 제거 검증)")

	url, _ := entry["url"].(string)
	assert.Equal(t, content.URL, url, "url 필드는 content.URL 과 일치")

	reason, _ := entry["reject_reason"].(string)
	require.NotEmpty(t, reason, "reject_reason 필드 존재")
	assert.Contains(t, reason, "VAL_")

	assert.Equal(t, "info", entry["level"])
}
