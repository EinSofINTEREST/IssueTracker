package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/handler"
	"issuetracker/internal/processor/fetcher/worker"
)

// fakeHandler 는 chromedp pool 의 *general.ChainHandler 가 아닌 임의 Handler 구현체를
// Registry 에 등록하여 ChromedpJobHandler 의 type assertion 실패 경로를 검증합니다.
// 실제 ChainHandler 는 chromedp 의존성이 있어 본 unit test 에서는 다루지 않습니다.
type fakeHandler struct{}

func (fakeHandler) Handle(_ context.Context, _ *core.CrawlJob) ([]*core.Content, error) {
	return nil, nil
}

// TestNewChromedpJobHandler_NilRegistry_ReturnsError 는 Registry nil 거부를 검증합니다 (이슈 #208).
func TestNewChromedpJobHandler_NilRegistry_ReturnsError(t *testing.T) {
	sem, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	_, err = worker.NewChromedpJobHandler(nil, []worker.Semaphore{sem}, nil)
	assert.Error(t, err)
}

// TestNewChromedpJobHandler_EmptySems_ReturnsError 는 빈 Semaphore slice 거부를 검증합니다
// (이슈 #229 — per-worker 모델은 최소 1 슬롯 필요).
func TestNewChromedpJobHandler_EmptySems_ReturnsError(t *testing.T) {
	reg := handler.NewRegistry(nil)

	_, err := worker.NewChromedpJobHandler(reg, nil, nil)
	assert.Error(t, err)

	_, err = worker.NewChromedpJobHandler(reg, []worker.Semaphore{}, nil)
	assert.Error(t, err)
}

// TestNewChromedpJobHandler_NilSemInSlice_ReturnsError 는 sems slice 안 nil 원소 거부를 검증합니다.
func TestNewChromedpJobHandler_NilSemInSlice_ReturnsError(t *testing.T) {
	reg := handler.NewRegistry(nil)
	sem, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	_, err = worker.NewChromedpJobHandler(reg, []worker.Semaphore{sem, nil}, nil)
	assert.Error(t, err)
}

// TestChromedpJobHandler_NilJob_ReturnsError 는 nil job 방어를 검증합니다.
func TestChromedpJobHandler_NilJob_ReturnsError(t *testing.T) {
	reg := handler.NewRegistry(nil)
	sem, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	h, err := worker.NewChromedpJobHandler(reg, []worker.Semaphore{sem}, nil)
	require.NoError(t, err)

	_, err = h.Handle(context.Background(), nil)
	assert.Error(t, err)
}

// TestChromedpJobHandler_UnregisteredCrawler_ReturnsError 는 Registry 에 등록되지 않은
// crawler_name 에 대해 error 를 반환하면서 Semaphore 는 정상 Acquire/Release 되는지 검증합니다.
func TestChromedpJobHandler_UnregisteredCrawler_ReturnsError(t *testing.T) {
	reg := handler.NewRegistry(nil)
	sem, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	h, err := worker.NewChromedpJobHandler(reg, []worker.Semaphore{sem}, nil)
	require.NoError(t, err)

	job := &core.CrawlJob{ID: "j1", CrawlerName: "missing", Target: core.Target{URL: "https://example.com"}}
	_, err = h.Handle(context.Background(), job)
	assert.Error(t, err)

	// Acquire/Release 가 짝을 이뤘다면 슬롯이 빈 상태 — 이어 Acquire 즉시 성공해야 함.
	require.NoError(t, sem.Acquire(context.Background()))
	require.NoError(t, sem.Release())
}

// TestChromedpJobHandler_NonChainHandler_ReturnsError 는 등록 Handler 가 *general.ChainHandler
// 가 아닐 때 error 를 반환하면서 Semaphore 는 정상 정리되는지 검증합니다.
func TestChromedpJobHandler_NonChainHandler_ReturnsError(t *testing.T) {
	reg := handler.NewRegistry(nil)
	reg.Register("noncha", fakeHandler{})

	sem, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	h, err := worker.NewChromedpJobHandler(reg, []worker.Semaphore{sem}, nil)
	require.NoError(t, err)

	job := &core.CrawlJob{ID: "j1", CrawlerName: "noncha", Target: core.Target{URL: "https://example.com"}}
	_, err = h.Handle(context.Background(), job)
	assert.Error(t, err)

	require.NoError(t, sem.Acquire(context.Background()))
	require.NoError(t, sem.Release())
}

// TestChromedpJobHandler_PerWorkerSemaphores_AcquireOnlyAssignedSlot 는 worker_id 가
// 자기 슬롯의 Semaphore 만 점유하고 다른 슬롯에 영향이 없는지 검증합니다 (이슈 #229 의 핵심 모델).
//
// 검증: worker 0 이 자기 슬롯 (capacity=1) 을 점유한 상태에서, worker 1 의 슬롯은 여전히 가용.
func TestChromedpJobHandler_PerWorkerSemaphores_AcquireOnlyAssignedSlot(t *testing.T) {
	reg := handler.NewRegistry(nil)

	sem0, err := worker.NewSemaphore(1)
	require.NoError(t, err)
	sem1, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	// worker 0 의 슬롯을 외부에서 미리 점유 — 글로벌 모델이라면 worker 1 도 차단되었어야 함.
	require.NoError(t, sem0.Acquire(context.Background()))

	h, err := worker.NewChromedpJobHandler(reg, []worker.Semaphore{sem0, sem1}, nil)
	require.NoError(t, err)

	// worker 1 ctx 로 Handle 호출 — Registry 미등록이라 lookup 단계에서 실패하지만,
	// 그 전에 sem1 은 정상 Acquire 후 Release 되어야 함.
	ctx := worker.WithWorkerID(context.Background(), 1)
	job := &core.CrawlJob{ID: "j1", CrawlerName: "missing", Target: core.Target{URL: "https://example.com"}}
	_, err = h.Handle(ctx, job)
	assert.Error(t, err) // unregistered → error

	// worker 1 의 슬롯이 deferred Release 로 빈 상태 — Acquire 즉시 성공.
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	assert.NoError(t, sem1.Acquire(timeoutCtx))
	require.NoError(t, sem1.Release())

	// worker 0 의 슬롯은 여전히 외부 점유 상태 그대로.
	require.NoError(t, sem0.Release())
}

// TestChromedpJobHandler_OutOfRangeWorkerID_FallbackToSlot0 는 worker_id 가 sems 범위를 벗어날 때
// 0번 슬롯으로 fallback 되는지 검증합니다 (방어 — out-of-range 면 0번 슬롯 점유 후 진행).
func TestChromedpJobHandler_OutOfRangeWorkerID_FallbackToSlot0(t *testing.T) {
	reg := handler.NewRegistry(nil)

	sem0, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	h, err := worker.NewChromedpJobHandler(reg, []worker.Semaphore{sem0}, nil)
	require.NoError(t, err)

	// worker_id=99 → sems 길이 1 → fallback to slot 0
	ctx := worker.WithWorkerID(context.Background(), 99)
	job := &core.CrawlJob{ID: "j1", CrawlerName: "missing", Target: core.Target{URL: "https://example.com"}}
	_, err = h.Handle(ctx, job)
	assert.Error(t, err) // unregistered → error

	// 0번 슬롯이 정상 정리되었는지 확인.
	require.NoError(t, sem0.Acquire(context.Background()))
	require.NoError(t, sem0.Release())
}

// TestChromedpJobHandler_NoWorkerID_FallbackToSlot0 는 ctx 에 worker_id 가 없을 때
// 0번 슬롯으로 fallback 되는지 검증합니다 (테스트 / 비-pool 호출 경로 호환성).
func TestChromedpJobHandler_NoWorkerID_FallbackToSlot0(t *testing.T) {
	reg := handler.NewRegistry(nil)

	sem0, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	h, err := worker.NewChromedpJobHandler(reg, []worker.Semaphore{sem0}, nil)
	require.NoError(t, err)

	job := &core.CrawlJob{ID: "j1", CrawlerName: "missing", Target: core.Target{URL: "https://example.com"}}
	_, err = h.Handle(context.Background(), job) // worker_id 미설정
	assert.Error(t, err)

	require.NoError(t, sem0.Acquire(context.Background()))
	require.NoError(t, sem0.Release())
}
