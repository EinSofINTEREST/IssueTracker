package rate_limiter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ratelimiter "issuetracker/internal/crawler/rate_limiter"
	"issuetracker/pkg/logger"
)

// debugContext는 debug 레벨 logger를 buf에 기록하는 context를 반환합니다.
func debugContext(buf *bytes.Buffer) context.Context {
	cfg := logger.DefaultConfig()
	cfg.Level = logger.LevelDebug
	cfg.Output = buf
	return logger.New(cfg).ToContext(context.Background())
}

// lastLog는 buf에서 마지막 JSON 로그 라인을 파싱해 반환합니다.
func lastLog(t *testing.T, buf *bytes.Buffer) map[string]interface{} {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(lines[len(lines)-1], &out))
	return out
}

// findLog는 buf에서 message가 일치하는 첫 번째 로그를 반환합니다.
func findLog(buf *bytes.Buffer, message string) map[string]interface{} {
	for _, line := range bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")) {
		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["message"] == message {
			return entry
		}
	}
	return nil
}

func TestNewRateLimiter(t *testing.T) {
	limiter := ratelimiter.NewRateLimiter(3600, 10)
	assert.NotNil(t, limiter)
}

func TestRateLimiter_Allow_InitialBurst(t *testing.T) {
	limiter := ratelimiter.NewRateLimiter(3600, 5)

	// 초기 burst만큼 허용되어야 함
	for i := 0; i < 5; i++ {
		assert.True(t, limiter.Allow(), "request %d should be allowed", i+1)
	}

	// burst 초과하면 거부
	assert.False(t, limiter.Allow())
}

func TestRateLimiter_Allow_Refill(t *testing.T) {
	// 시간당 3600 요청 = 초당 1 요청
	limiter := ratelimiter.NewRateLimiter(3600, 1)

	assert.True(t, limiter.Allow())

	// 즉시 두 번째 요청은 거부
	assert.False(t, limiter.Allow())

	// 1초 대기 후 refill
	time.Sleep(1100 * time.Millisecond)

	assert.True(t, limiter.Allow())
}

func TestRateLimiter_Wait_Success(t *testing.T) {
	limiter := ratelimiter.NewRateLimiter(3600, 2)
	ctx := context.Background()

	// 처음 2개는 즉시 허용
	require.NoError(t, limiter.Wait(ctx))
	require.NoError(t, limiter.Wait(ctx))

	// 세 번째는 대기 필요
	start := time.Now()
	require.NoError(t, limiter.Wait(ctx))
	elapsed := time.Since(start)

	// 최소 0.5초 이상 대기해야 함
	assert.Greater(t, elapsed, 500*time.Millisecond)
}

func TestRateLimiter_Wait_ContextCanceled(t *testing.T) {
	limiter := ratelimiter.NewRateLimiter(10, 1) // 낮은 rate로 설정
	limiter.Allow()                              // 토큰 소진

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := limiter.Wait(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRateLimiter_Wait_ContextTimeout(t *testing.T) {
	limiter := ratelimiter.NewRateLimiter(10, 1) // 낮은 rate로 설정
	limiter.Allow()                              // 토큰 소진

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := limiter.Wait(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRateLimiter_ConcurrentRequests(t *testing.T) {
	limiter := ratelimiter.NewRateLimiter(100, 10)

	// 동시에 20개 요청
	var allowed atomic.Int32
	var denied atomic.Int32

	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func() {
			if limiter.Allow() {
				allowed.Add(1)
			} else {
				denied.Add(1)
			}
			done <- true
		}()
	}

	for i := 0; i < 20; i++ {
		<-done
	}

	// Burst는 10이므로 최대 10개만 허용
	assert.LessOrEqual(t, allowed.Load(), int32(10))
	assert.GreaterOrEqual(t, denied.Load(), int32(10))
}

func TestTokenBucketRateLimiter_String(t *testing.T) {
	limiter := ratelimiter.NewRateLimiter(3600, 10)
	str := limiter.(*ratelimiter.TokenBucketRateLimiter).String()

	assert.Contains(t, str, "RateLimiter")
	assert.Contains(t, str, "rate=1.00/s")
	assert.Contains(t, str, "burst=10")
}

func TestRateLimiter_Wait_LogsWaitStart(t *testing.T) {
	var buf bytes.Buffer
	ctx := debugContext(&buf)

	limiter := ratelimiter.NewRateLimiter(3600, 1)
	require.NoError(t, limiter.Wait(ctx)) // 첫 번째 — 즉시 통과
	buf.Reset()

	require.NoError(t, limiter.Wait(ctx)) // 두 번째 — 대기 발생

	entry := findLog(&buf, "rate limit reached, waiting for token")
	require.NotNil(t, entry, "rate limit reached 로그가 있어야 합니다")
	assert.Contains(t, entry, "wait_ms")
	assert.Contains(t, entry, "rate")
	assert.Contains(t, entry, "burst")
}

func TestRateLimiter_Wait_LogsWaitCompleted(t *testing.T) {
	var buf bytes.Buffer
	ctx := debugContext(&buf)

	limiter := ratelimiter.NewRateLimiter(3600, 1)
	require.NoError(t, limiter.Wait(ctx)) // 첫 번째 — 즉시 통과
	buf.Reset()

	require.NoError(t, limiter.Wait(ctx)) // 두 번째 — 대기 후 완료

	entry := findLog(&buf, "rate limit wait completed")
	require.NotNil(t, entry, "rate limit wait completed 로그가 있어야 합니다")
	assert.Contains(t, entry, "wait_count")
}

func TestRateLimiter_Wait_LogsContextCancelled(t *testing.T) {
	var buf bytes.Buffer

	limiter := ratelimiter.NewRateLimiter(10, 1)
	limiter.Allow() // 토큰 소진

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logCfg := logger.DefaultConfig()
	logCfg.Level = logger.LevelDebug
	logCfg.Output = &buf
	ctx = logger.New(logCfg).ToContext(ctx)

	err := limiter.Wait(ctx)
	require.ErrorIs(t, err, context.Canceled)

	entry := lastLog(t, &buf)
	assert.Equal(t, "debug", entry["level"])
	assert.Equal(t, "rate limit wait context done", entry["message"])
	assert.Contains(t, entry, "wait_count")
	assert.Contains(t, entry, "rate")
	assert.Contains(t, entry, "burst")
}
