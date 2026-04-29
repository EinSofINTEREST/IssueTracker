package worker_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	parserWorker "issuetracker/internal/parser/worker"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// fakeRawSvc 는 RawContentService 의 in-memory mock 입니다.
// Purge / Delete / Get 호출 횟수를 atomic 으로 추적.
type fakeRawSvc struct {
	purgeCalls   int64
	deleteCalls  int64
	getCalls     int64
	purgeReturns int64
	purgeErr     error
}

func (s *fakeRawSvc) Store(_ context.Context, _ *core.RawContent) (string, bool, error) {
	return "", false, nil
}
func (s *fakeRawSvc) GetByID(_ context.Context, _ string) (*core.RawContent, error) {
	atomic.AddInt64(&s.getCalls, 1)
	return nil, storage.ErrNotFound
}
func (s *fakeRawSvc) Delete(_ context.Context, _ string) error {
	atomic.AddInt64(&s.deleteCalls, 1)
	return nil
}
func (s *fakeRawSvc) List(_ context.Context, _ storage.RawContentFilter) ([]*core.RawContent, error) {
	return nil, nil
}
func (s *fakeRawSvc) PurgeOlderThan(_ context.Context, _ time.Time) (int64, error) {
	atomic.AddInt64(&s.purgeCalls, 1)
	if s.purgeErr != nil {
		return 0, s.purgeErr
	}
	return s.purgeReturns, nil
}

func TestRawContentCleaner_PurgesPeriodically(t *testing.T) {
	svc := &fakeRawSvc{purgeReturns: 3}
	cleaner := parserWorker.NewRawContentCleaner(svc, parserWorker.CleanupConfig{
		Interval: 50 * time.Millisecond,
		StaleTTL: 100 * time.Millisecond,
	}, logger.New(logger.DefaultConfig()))

	ctx, cancel := context.WithCancel(context.Background())
	cleaner.Start(ctx)

	// 3 ticks 정도 흐를 시간
	time.Sleep(180 * time.Millisecond)
	cancel()
	cleaner.Stop()

	calls := atomic.LoadInt64(&svc.purgeCalls)
	assert.GreaterOrEqual(t, calls, int64(2), "30ms 마다 ticker → 180ms 동안 최소 2회 purge")
}

func TestRawContentCleaner_HonorsContextCancel(t *testing.T) {
	svc := &fakeRawSvc{purgeErr: errors.New("simulated db error")}
	cleaner := parserWorker.NewRawContentCleaner(svc, parserWorker.CleanupConfig{
		Interval: 30 * time.Millisecond,
	}, logger.New(logger.DefaultConfig()))

	ctx, cancel := context.WithCancel(context.Background())
	cleaner.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Stop 이 무한 대기 안 하는지 검증
	done := make(chan struct{})
	go func() {
		cleaner.Stop()
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(1 * time.Second):
		t.Fatal("cleaner.Stop did not return after ctx cancel")
	}
}

func TestRawContentCleaner_DefaultsApplied(t *testing.T) {
	svc := &fakeRawSvc{}
	cleaner := parserWorker.NewRawContentCleaner(svc, parserWorker.CleanupConfig{}, logger.New(logger.DefaultConfig()))
	require.NotNil(t, cleaner, "default config 로도 정상 생성")

	// 즉시 종료 — defaults 가 적용된 상태로 leak 없이 stop
	ctx, cancel := context.WithCancel(context.Background())
	cleaner.Start(ctx)
	cancel()
	cleaner.Stop()
}
