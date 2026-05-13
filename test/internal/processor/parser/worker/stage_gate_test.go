package worker_test

// stage_gate_test.go — PR #358 리뷰 (CodeRabbit / Copilot / gemini) 반영 테스트.
//
// 검증 항목:
//  1. acquired=false → errStageGateNotAcquired sentinel 반환 (commit 안 함 의도)
//  2. gateErr + ctx.Err()!=nil → wrapped gateErr 반환 (commit 안 함 + ctx abort)
//  3. gateErr + ctx ok → fail-open 진행 (rawSvc 까지 도달)
//  4. acquired=true → release 호출 보장

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	parserWorker "issuetracker/internal/processor/parser/worker"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// fakeStageGate 는 StageGate 의 black-box stub 입니다.
type fakeStageGate struct {
	acquireCalls int64
	releaseCalls int64
	// 반환값 설정
	acquired bool
	err      error
}

func (g *fakeStageGate) Acquire(_ context.Context, _ string) (func(), bool, error) {
	atomic.AddInt64(&g.acquireCalls, 1)
	if g.err != nil {
		return nil, false, g.err
	}
	if !g.acquired {
		return nil, false, nil
	}
	return func() { atomic.AddInt64(&g.releaseCalls, 1) }, true, nil
}

// newGatedWorker 는 gate + rawSvc 를 주입한 ParserWorker 를 생성합니다.
func newGatedWorker(gate locks.StageGate, rawSvc *fakeRawSvc, log *logger.Logger) *parserWorker.ParserWorker {
	return parserWorker.NewParserWorker(
		nil,    // consumer
		nil,    // pub
		rawSvc, // rawSvc
		nil,    // contentSvc
		nil,    // parser
		nil,    // resolver
		nil,    // sampleSvc
		gate,   // stage gate
		nil,    // llmGen
		nil,    // failureCounter
		nil,    // rawIDTracker
		nil,    // upgrader
		0,      // emptyBodyTitleMin
		0,      // emptyBodyContentMin
		1,      // workerCount
		log,
	)
}

func newMsgForURL(t *testing.T, id, url string) *queue.Message {
	t.Helper()
	ref := core.RawContentRef{
		ID:        id,
		URL:       url,
		FetchedAt: time.Now(),
		SourceInfo: core.SourceInfo{
			Name:    "test-crawler",
			Country: "KR",
		},
	}
	body, err := json.Marshal(ref)
	require.NoError(t, err)
	return &queue.Message{
		Topic:   queue.TopicFetched,
		Value:   body,
		Headers: map[string]string{"crawler": "test-crawler"},
	}
}

// TestProcessMessage_GateNotAcquired_ReturnsSentinel 은 gate.Acquire 가 (nil, false, nil)
// 을 반환하면 (다른 worker 가 lock 보유) processMessage 가 errStageGateNotAcquired 를
// 반환하여 commit 을 회피하는지 검증합니다. (PR #358 CodeRabbit 반영)
func TestProcessMessage_GateNotAcquired_ReturnsSentinel(t *testing.T) {
	gate := &fakeStageGate{acquired: false, err: nil}
	rawSvc := &fakeRawSvc{}
	log := logger.New(logger.DefaultConfig())

	pw := newGatedWorker(gate, rawSvc, log)
	msg := newMsgForURL(t, "raw-001", "https://example.com/a")

	err := pw.ProcessMessage(context.Background(), msg)

	require.Error(t, err)
	assert.True(t, errors.Is(err, parserWorker.ErrStageGateNotAcquired),
		"acquired=false 시 sentinel 반환 필요")
	assert.Equal(t, int64(1), atomic.LoadInt64(&gate.acquireCalls))
	assert.Equal(t, int64(0), atomic.LoadInt64(&gate.releaseCalls), "획득 실패 시 release 호출 X")
	assert.Equal(t, int64(0), atomic.LoadInt64(&rawSvc.getCalls), "lock 미획득 시 rawSvc 미도달")
}

// TestProcessMessage_GateErrWithCtxCancel_ReturnsError 는 gate.Acquire 가 ctx cancel/deadline
// 으로 인해 error 를 반환하면 processMessage 가 wrapped error 를 반환하여 commit 을
// 회피하는지 검증합니다. (PR #358 gemini / Copilot 반영)
func TestProcessMessage_GateErrWithCtxCancel_ReturnsError(t *testing.T) {
	gate := &fakeStageGate{err: context.Canceled}
	rawSvc := &fakeRawSvc{}
	log := logger.New(logger.DefaultConfig())

	pw := newGatedWorker(gate, rawSvc, log)
	msg := newMsgForURL(t, "raw-002", "https://example.com/b")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ctx 종료 상태로 호출

	err := pw.ProcessMessage(ctx, msg)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "ctx cancel 원인 err 보존")
	assert.False(t, errors.Is(err, parserWorker.ErrStageGateNotAcquired),
		"sentinel 이 아닌 일반 error 로 분류")
	assert.Equal(t, int64(0), atomic.LoadInt64(&rawSvc.getCalls), "ctx cancel 시 rawSvc 미도달")
}

// TestProcessMessage_GateErrInfraFailureProceeds 는 gate.Acquire 가 ctx 정상 상태에서 인프라
// 에러 (예: Redis 장애) 를 반환하면 fail-open 으로 rawSvc 까지 진행하는지 검증합니다.
// (PR #358 Copilot 반영 — 인프라 에러는 graceful degrade 유지)
func TestProcessMessage_GateErrInfraFailureProceeds(t *testing.T) {
	infraErr := errors.New("redis: connection refused")
	gate := &fakeStageGate{err: infraErr}
	rawSvc := &fakeRawSvc{} // GetByID 는 ErrNotFound 반환 → processMessage 가 정상 종료 (nil)
	log := logger.New(logger.DefaultConfig())

	pw := newGatedWorker(gate, rawSvc, log)
	msg := newMsgForURL(t, "raw-003", "https://example.com/c")

	err := pw.ProcessMessage(context.Background(), msg)

	// rawSvc.GetByID 가 ErrNotFound 반환 → processMessage 가 nil 반환 (정상 skip).
	// 중요한 건 rawSvc 까지 도달했다는 사실 (fail-open).
	assert.NoError(t, err, "인프라 에러 + ctx ok → fail-open 진행 후 정상 종료")
	assert.Equal(t, int64(1), atomic.LoadInt64(&rawSvc.getCalls), "fail-open 시 rawSvc.GetByID 도달 필요")
}

// TestProcessMessage_GateAcquired_ReleasesAfter 는 acquired=true 시 processMessage 종료 후
// release 가 호출되는지 (defer release()) 검증합니다.
func TestProcessMessage_GateAcquired_ReleasesAfter(t *testing.T) {
	gate := &fakeStageGate{acquired: true, err: nil}
	rawSvc := &fakeRawSvc{} // GetByID → ErrNotFound 로 빠르게 종료
	log := logger.New(logger.DefaultConfig())

	pw := newGatedWorker(gate, rawSvc, log)
	msg := newMsgForURL(t, "raw-004", "https://example.com/d")

	err := pw.ProcessMessage(context.Background(), msg)

	assert.NoError(t, err)
	assert.Equal(t, int64(1), atomic.LoadInt64(&gate.acquireCalls))
	assert.Equal(t, int64(1), atomic.LoadInt64(&gate.releaseCalls), "성공 후 release 1회 보장")
	assert.Equal(t, int64(1), atomic.LoadInt64(&rawSvc.getCalls), "정상 acquire 시 rawSvc 도달")
}

// 컴파일 타임 contract — storage.ErrNotFound 가 변경되어도 import 가 유지되도록.
var _ = storage.ErrNotFound
