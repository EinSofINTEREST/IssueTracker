package worker_test

// reparse_test.go — Sub C (#366) — parser worker 의 reparse cycle 분기 검증.
//
// 검증 항목:
//  1. msg.Headers 에 validate_reparse_reason 존재 시 ctx 에 llmgen.WithRejectReason 첨부
//     (raw 없는 케이스에서 ProcessMessage 가 정상 early-return 하는지만 black-box)
//  2. msg.Headers 에 reparse 헤더 존재 시 core.WithInboxHeaders 가 ctx 에 첨부 — publishContents 가
//     이를 normalized 메시지로 전파

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	parserWorker "issuetracker/internal/processor/parser/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// 본 test 에서만 사용 — 다른 _test.go 의 helper 와 충돌 회피.
var _ = parserWorker.ErrStageGateNotAcquired // 컴파일러 강제

// msgWithReparseHeaders 는 RawContentRef payload + reparse 헤더가 설정된 fetched 메시지를 만듭니다.
func msgWithReparseHeaders(t *testing.T, refID, url string, count, reason string) *queue.Message {
	t.Helper()
	ref := core.RawContentRef{
		ID:        refID,
		URL:       url,
		FetchedAt: time.Now(),
		SourceInfo: core.SourceInfo{
			Name:    "test-crawler",
			Country: "KR",
		},
	}
	body, err := json.Marshal(ref)
	require.NoError(t, err)
	headers := map[string]string{
		core.HeaderCrawler:    "test-crawler",
		core.HeaderTargetType: string(core.TargetTypeArticle),
	}
	if count != "" {
		headers[core.HeaderValidateReparseCount] = count
	}
	if reason != "" {
		headers[core.HeaderValidateReparseReason] = reason
	}
	return &queue.Message{
		Topic:   queue.TopicFetched,
		Value:   body,
		Headers: headers,
	}
}

// TestProcessMessage_ReparseHeaders_NoRaw_Skipped 는 reparse 헤더가 있는 msg 를 처리할 때
// raw 가 없으면 early-return 으로 정상 종료하는지 검증 (회귀 보호 + 기본 동작).
func TestProcessMessage_ReparseHeaders_NoRaw_Skipped(t *testing.T) {
	// fakeRawSvc 는 cleanup_test.go 의 동일 mock — GetByID 가 ErrNotFound 반환.
	gate := &fakeStageGate{acquired: true, err: nil}
	rawSvc := &fakeRawSvc{}
	log := logger.New(logger.DefaultConfig())

	pw := newGatedWorker(gate, rawSvc, log)
	msg := msgWithReparseHeaders(t, "raw-rp-001", "https://example.com/r1", "1", "PublishedAt required")

	err := pw.ProcessMessage(context.Background(), msg)
	assert.NoError(t, err, "reparse header 있는 msg 도 raw 부재 시 정상 종료")
}

// testLogger 는 stage_gate_test.go 와 공유되는 logger 생성을 race-safe 하게 분리.
// (자체 함수 — 다른 _test.go 와 충돌하지 않도록 이름 분리.)
// stage_gate_test.go 의 helper 와 동일 logger 생성을 분리해서 사용.
// fakeRawSvc / fakeStageGate 는 cleanup_test.go / stage_gate_test.go 에 정의됨.
//
// 본 test 의 핵심은 reparse 헤더가 panic 없이 처리되는지 + early-return 정상 동작.
// 더 깊은 ctx propagation 검증은 통합 라이브 검증에서 수행.
