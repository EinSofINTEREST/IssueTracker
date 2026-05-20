// PriorityRulesRefresher 의 부팅 hydrate + 주기 refresh + loader 에러 처리 검증 (이슈 #521).
package bus_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
)

func TestPriorityRulesRefresher_BootHydrate_AppliesImmediately(t *testing.T) {
	resolver := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)

	loader := func(_ context.Context) ([]bus.HostPathPriorityRule, error) {
		return []bus.HostPathPriorityRule{
			{HostPattern: "boot.example.com", PathPattern: "", Priority: core.PriorityHigh},
		}, nil
	}

	// interval 을 길게 설정 — 부팅 hydrate 만 검증.
	refresher := bus.NewPriorityRulesRefresher(resolver, loader, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refresher.Start(ctx)

	// Start 가 즉시 refreshOnce 호출 — 룰이 적용되어 있어야 함.
	job := &core.CrawlJob{Target: core.Target{URL: "https://boot.example.com/"}}
	assert.Equal(t, core.PriorityHigh, resolver.Resolve(job))
}

func TestPriorityRulesRefresher_LoaderError_RetainsPreviousRules(t *testing.T) {
	resolver := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	// 초기 룰 직접 주입.
	resolver.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "prev.example.com", PathPattern: "", Priority: core.PriorityLow},
	})

	loader := func(_ context.Context) ([]bus.HostPathPriorityRule, error) {
		return nil, errors.New("db connection lost")
	}

	refresher := bus.NewPriorityRulesRefresher(resolver, loader, time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refresher.Start(ctx)

	// 에러 시 기존 룰 유지 — prev.example.com 가 여전히 Low 로 매칭.
	job := &core.CrawlJob{Target: core.Target{URL: "https://prev.example.com/x"}}
	assert.Equal(t, core.PriorityLow, resolver.Resolve(job))
}

func TestPriorityRulesRefresher_PeriodicRefresh_PicksUpChanges(t *testing.T) {
	resolver := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)

	var callCount int32
	loader := func(_ context.Context) ([]bus.HostPathPriorityRule, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return []bus.HostPathPriorityRule{
				{HostPattern: "first.example.com", PathPattern: "", Priority: core.PriorityHigh},
			}, nil
		}
		return []bus.HostPathPriorityRule{
			{HostPattern: "second.example.com", PathPattern: "", Priority: core.PriorityLow},
		}, nil
	}

	// 짧은 interval — ticker 가 한 번은 발화하도록.
	refresher := bus.NewPriorityRulesRefresher(resolver, loader, 50*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refresher.Start(ctx)

	// 부팅 호출 1번 — first 룰.
	firstJob := &core.CrawlJob{Target: core.Target{URL: "https://first.example.com/"}}
	assert.Equal(t, core.PriorityHigh, resolver.Resolve(firstJob))

	// ticker 발화 대기 (50ms × 3 = 150ms 안에 최소 2번 더 호출).
	time.Sleep(200 * time.Millisecond)

	// 이제 second 룰 적용 — first 미매칭.
	assert.Equal(t, core.PriorityNormal, resolver.Resolve(firstJob))
	secondJob := &core.CrawlJob{Target: core.Target{URL: "https://second.example.com/x"}}
	assert.Equal(t, core.PriorityLow, resolver.Resolve(secondJob))
}

func TestPriorityRulesRefresher_CtxCancel_StopsTicker(t *testing.T) {
	resolver := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)

	var callCount int32
	loader := func(_ context.Context) ([]bus.HostPathPriorityRule, error) {
		atomic.AddInt32(&callCount, 1)
		return nil, nil
	}

	refresher := bus.NewPriorityRulesRefresher(resolver, loader, 30*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	refresher.Start(ctx)

	// 부팅 호출 1번 발생 후 즉시 cancel.
	cancel()

	// cancel 후 ticker 가 종료되어 추가 호출 없음 — 짧은 sleep 후 카운트 안정.
	time.Sleep(100 * time.Millisecond)
	stableCount := atomic.LoadInt32(&callCount)
	time.Sleep(100 * time.Millisecond)
	finalCount := atomic.LoadInt32(&callCount)
	assert.Equal(t, stableCount, finalCount, "ticker should not fire after ctx cancel")
}

func TestNewPriorityRulesRefresher_ZeroInterval_UsesDefault(t *testing.T) {
	// interval <= 0 → 5분 default — 사이드이펙트 없는 정합성 검증 (panic 안 함).
	resolver := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	loader := func(_ context.Context) ([]bus.HostPathPriorityRule, error) { return nil, nil }

	refresher := bus.NewPriorityRulesRefresher(resolver, loader, 0, nil)
	assert.NotNil(t, refresher)
}
