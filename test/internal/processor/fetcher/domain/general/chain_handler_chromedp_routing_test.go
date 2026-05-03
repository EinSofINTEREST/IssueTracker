package general_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// 이슈 #230 — ChainHandler.HandleChromedpOnly 의 worker_id 별 chromedpChains slice 라우팅 검증.
//
// 라우팅 검증 전략 (black-box):
//   - fake chain 각 인스턴스가 식별 가능한 sentinel error 반환 — Handle 호출 후 error 메시지로
//     어느 chain (worker_id) 이 실행됐는지 추적
//   - chain.Handle 이 error 반환 시 ChainHandler 는 raw store / publish 호출 안 함 → stub
//     RawSvc / Producer 메소드는 panic 으로 invariant 강제

// fakeChain 은 식별 가능한 sentinel error 를 반환하는 general.Handler 스텁.
type fakeChain struct {
	id string
}

func (f *fakeChain) Handle(_ context.Context, _ *core.CrawlJob) (*core.RawContent, error) {
	return nil, errors.New("fake-" + f.id)
}

// SetNext 는 chain 의 다음 노드 설정 — 본 테스트에서는 사용 안 함 (단일 노드 chain 시뮬레이션).
func (*fakeChain) SetNext(_ general.Handler) {}

// stubRawSvc 는 invariant 검증용 — 호출되면 안 됨 (chain.Handle error → 즉시 return).
type stubRawSvc struct{}

func (stubRawSvc) Store(_ context.Context, _ *core.RawContent) (string, bool, error) {
	panic("stubRawSvc.Store should not be called")
}
func (stubRawSvc) GetByID(_ context.Context, _ string) (*core.RawContent, error) {
	panic("stubRawSvc.GetByID should not be called")
}
func (stubRawSvc) Delete(_ context.Context, _ string) error {
	panic("stubRawSvc.Delete should not be called")
}
func (stubRawSvc) List(_ context.Context, _ storage.RawContentFilter) ([]*core.RawContent, error) {
	panic("stubRawSvc.List should not be called")
}
func (stubRawSvc) PurgeOlderThan(_ context.Context, _ time.Time) (int64, error) {
	panic("stubRawSvc.PurgeOlderThan should not be called")
}

type stubProducer struct{}

func (stubProducer) Publish(_ context.Context, _ queue.Message) error {
	panic("stubProducer.Publish should not be called")
}
func (stubProducer) PublishBatch(_ context.Context, _ []queue.Message) error {
	panic("stubProducer.PublishBatch should not be called")
}
func (stubProducer) Close() error { return nil }

func newRoutingHandler(t *testing.T, chains []general.Handler) *general.ChainHandler {
	t.Helper()
	log := logger.New(logger.DefaultConfig())
	defaultChain := &fakeChain{id: "default"} // 본 테스트에서는 호출되지 않음
	return general.NewChainHandler(
		nil, // SourceCrawler — HandleChromedpOnly 경로에서 사용 X
		defaultChain,
		chains,
		nil,
		stubRawSvc{},
		stubProducer{},
		log,
	)
}

func newJob() *core.CrawlJob {
	return &core.CrawlJob{
		ID:          "j1",
		CrawlerName: "test-crawler",
		Target:      core.Target{URL: "https://example.com/article"},
	}
}

// TestHandleChromedpOnly_NoChains_ReturnsError 는 ChromedpChains 가 nil 이면 graceful error 검증.
func TestHandleChromedpOnly_NoChains_ReturnsError(t *testing.T) {
	h := newRoutingHandler(t, nil)
	_, err := h.HandleChromedpOnly(context.Background(), newJob())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no chromedp chain wired")
}

// TestHandleChromedpOnly_RoutesByWorkerID 는 각 worker_id 가 자기 슬롯의 chain 을 호출하는지 검증.
func TestHandleChromedpOnly_RoutesByWorkerID(t *testing.T) {
	chains := []general.Handler{
		&fakeChain{id: "0"},
		&fakeChain{id: "1"},
		&fakeChain{id: "2"},
	}
	h := newRoutingHandler(t, chains)

	for id := 0; id < len(chains); id++ {
		t.Run(fmt.Sprintf("worker_id=%d", id), func(t *testing.T) {
			ctx := core.WithWorkerID(context.Background(), id)
			_, err := h.HandleChromedpOnly(ctx, newJob())
			require.Error(t, err)
			want := fmt.Sprintf("fake-%d", id)
			assert.True(t, strings.Contains(err.Error(), want),
				"worker_id=%d 의 error %q 에 %q 포함되어야 함", id, err.Error(), want)
		})
	}
}

// TestHandleChromedpOnly_OutOfRangeWorkerID_FallbackToSlot0 는 worker_id 가 chains 범위를 벗어날 때
// 0번 chain 으로 fallback 되는지 검증합니다 (방어).
func TestHandleChromedpOnly_OutOfRangeWorkerID_FallbackToSlot0(t *testing.T) {
	chains := []general.Handler{
		&fakeChain{id: "0"},
	}
	h := newRoutingHandler(t, chains)

	ctx := core.WithWorkerID(context.Background(), 99)
	_, err := h.HandleChromedpOnly(ctx, newJob())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "fake-0"),
		"out-of-range worker_id 는 slot 0 으로 fallback 되어야 함, got %q", err.Error())
}

// TestHandleChromedpOnly_NoWorkerID_FallbackToSlot0 는 ctx 에 worker_id 미설정 시 0번 chain fallback.
func TestHandleChromedpOnly_NoWorkerID_FallbackToSlot0(t *testing.T) {
	chains := []general.Handler{
		&fakeChain{id: "0"},
		&fakeChain{id: "1"},
	}
	h := newRoutingHandler(t, chains)

	_, err := h.HandleChromedpOnly(context.Background(), newJob())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "fake-0"),
		"worker_id 미설정 시 slot 0 fallback 되어야 함, got %q", err.Error())
}
