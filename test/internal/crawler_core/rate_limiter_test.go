package core_test

import (
	core "issuetracker/internal/crawler/core"

	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRateLimiter(t *testing.T) {
	limiter := core.NewRateLimiter(3600, 10)
	assert.NotNil(t, limiter)
}

func TestRateLimiter_Allow_InitialBurst(t *testing.T) {
	limiter := core.NewRateLimiter(3600, 5)

	// 초기 burst만큼 허용되어야 함
	for i := 0; i < 5; i++ {
		assert.True(t, limiter.Allow(), "request %d should be allowed", i+1)
	}

	// burst 초과하면 거부
	assert.False(t, limiter.Allow())
}

func TestRateLimiter_Allow_Refill(t *testing.T) {
	// 시간당 3600 요청 = 초당 1 요청
	limiter := core.NewRateLimiter(3600, 1)

	// 첫 번째 요청 허용
	assert.True(t, limiter.Allow())

	// 즉시 두 번째 요청은 거부
	assert.False(t, limiter.Allow())

	// 1초 대기 후 refill
	time.Sleep(1100 * time.Millisecond)

	// 이제 허용되어야 함
	assert.True(t, limiter.Allow())
}

func TestRateLimiter_Wait_Success(t *testing.T) {
	limiter := core.NewRateLimiter(3600, 2)
	ctx := context.Background()

	// 처음 2개는 즉시 허용
	err := limiter.Wait(ctx)
	require.NoError(t, err)

	err = limiter.Wait(ctx)
	require.NoError(t, err)

	// 세 번째는 대기 필요
	start := time.Now()
	err = limiter.Wait(ctx)
	elapsed := time.Since(start)

	require.NoError(t, err)
	// 최소 0.5초 이상 대기해야 함 (1초의 여유를 둠)
	assert.Greater(t, elapsed, 500*time.Millisecond)
}

func TestRateLimiter_Wait_ContextCanceled(t *testing.T) {
	limiter := core.NewRateLimiter(10, 1) // 낮은 rate로 설정

	// Token 소진
	limiter.Allow()

	// Context cancel
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := limiter.Wait(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRateLimiter_Wait_ContextTimeout(t *testing.T) {
	limiter := core.NewRateLimiter(10, 1) // 낮은 rate로 설정

	// Token 소진
	limiter.Allow()

	// 짧은 timeout 설정
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := limiter.Wait(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRateLimiter_ConcurrentRequests(t *testing.T) {
	limiter := core.NewRateLimiter(100, 10)

	// 동시에 20개 요청
	allowed := 0
	denied := 0

	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func() {
			if limiter.Allow() {
				allowed++
			} else {
				denied++
			}
			done <- true
		}()
	}

	// 모든 고루틴 완료 대기
	for i := 0; i < 20; i++ {
		<-done
	}

	// Burst는 10이므로 최대 10개만 허용
	assert.LessOrEqual(t, allowed, 10)
	assert.GreaterOrEqual(t, denied, 10)
}

func TestTokenBucketRateLimiter_String(t *testing.T) {
	limiter := core.NewRateLimiter(3600, 10)
	str := limiter.(*core.TokenBucketRateLimiter).String()

	assert.Contains(t, str, "RateLimiter")
	assert.Contains(t, str, "rate=1.00/s")
	assert.Contains(t, str, "burst=10")
}
