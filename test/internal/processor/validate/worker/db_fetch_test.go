package worker_test

// db_fetch_test.go — 이슈 #648: DB 조회 실패의 일시성 분류.
//
// query timeout 은 재시도(미커밋)로, 그 외 DB 에러는 기존대로 DLQ 로 간다.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/queue"
)

// DB 가 잠깐 흔들린 사이 토픽이 통째로 DLQ 에 쌓이면, 전부 정상 commit 이라 lag 도 알람도
// 뜨지 않는다. DLQ replay 도 아직 없어 (이슈 #559) 사실상 회수 불가다.
func TestValidateWorker_DBQueryTimeout_RetriesInsteadOfDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := newMockContentService()
	log, buf := captureLog()
	w := newWorkerWithLogger(consumer, producer, contentSvc, log)

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	// QueryTimeout decorator (이슈 #427) 가 내보내는 형태 — context.DeadlineExceeded 를 wrap.
	timeoutErr := fmt.Errorf("query content by id: %w", context.DeadlineExceeded)
	contentSvc.On("GetByID", mock.Anything, content.ID).Return(nil, timeoutErr)

	runWorkerWithLogger(t, consumer, w, msg, log)

	entry := findLogEntry(t, buf, "db query timed out fetching content, retrying")
	assert.Equal(t, "warn", entry["level"], "일시 장애는 error 가 아니라 warn 이어야 합니다")
	assert.Equal(t, content.ID, entry["ref_id"])

	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// timeout 이 아닌 DB 에러는 기존 동작 유지 — 일시성 판단이 서지 않는 것을 무한정 되돌리면
// 영구 실패 row 가 루프에 갇힌다 (재시도 상한은 이슈 #603 과 함께 결정).
func TestValidateWorker_NonTimeoutDBError_StillGoesToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := newMockContentService()
	log, buf := captureLog()
	w := newWorkerWithLogger(consumer, producer, contentSvc, log)

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).
		Return(nil, errors.New("pq: syntax error at or near \"SELCT\""))
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorkerWithLogger(t, consumer, w, msg, log)

	findLogEntry(t, buf, "failed to fetch content from db, sending to dlq")

	producer.AssertCalled(t, "Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	}))
	require.NotContains(t, buf.String(), "db query timed out")
}
