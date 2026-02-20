package core

import (
  "context"
  "fmt"
  "sync"
  "time"

  "ecoscrapper/pkg/logger"
)

// TokenBucketRateLimiter는 token bucket 알고리즘을 사용한 rate limiter입니다.
// 시간당 요청 수를 제한하며, burst를 허용합니다.
type TokenBucketRateLimiter struct {
  rate       float64       // tokens per second
  burst      int           // maximum tokens
  tokens     float64       // current tokens
  lastRefill time.Time
  mu         sync.Mutex
}

// NewRateLimiter는 새로운 rate limiter를 생성합니다.
// requestsPerHour: 시간당 허용 요청 수
// burst: 한번에 허용되는 최대 요청 수
func NewRateLimiter(requestsPerHour, burst int) RateLimiter {
  rate := float64(requestsPerHour) / 3600.0 // convert to per second

  return &TokenBucketRateLimiter{
    rate:       rate,
    burst:      burst,
    tokens:     float64(burst),
    lastRefill: time.Now(),
  }
}

// Wait는 rate limit에 따라 대기합니다.
// token이 없으면 token이 생성될 때까지 대기합니다.
func (r *TokenBucketRateLimiter) Wait(ctx context.Context) error {
  log := logger.FromContext(ctx)
  waitStarted := false

  for {
    if r.Allow() {
      if waitStarted {
        log.Debug("rate limit wait completed")
      }
      return nil
    }

    // 다음 token이 생성될 때까지 대기
    sleepDuration := r.timeToNextToken()

    if !waitStarted {
      log.WithField("wait_ms", sleepDuration.Milliseconds()).
        Debug("rate limit reached, waiting for token")
      waitStarted = true
    }

    select {
    case <-ctx.Done():
      return ctx.Err()
    case <-time.After(sleepDuration):
      // continue to check again
    }
  }
}

// Allow는 현재 요청이 허용되는지 확인합니다.
// token이 있으면 true를 반환하고 token을 소비합니다.
func (r *TokenBucketRateLimiter) Allow() bool {
  r.mu.Lock()
  defer r.mu.Unlock()

  r.refill()

  if r.tokens >= 1.0 {
    r.tokens--
    return true
  }

  return false
}

// refill은 경과 시간에 따라 token을 채웁니다.
// 호출 전에 lock을 획득해야 합니다.
func (r *TokenBucketRateLimiter) refill() {
  now := time.Now()
  elapsed := now.Sub(r.lastRefill).Seconds()

  // 경과 시간에 비례하여 token 추가
  newTokens := elapsed * r.rate
  r.tokens = min(r.tokens+newTokens, float64(r.burst))
  r.lastRefill = now
}

// timeToNextToken은 다음 token이 생성될 때까지 시간을 반환합니다.
func (r *TokenBucketRateLimiter) timeToNextToken() time.Duration {
  r.mu.Lock()
  defer r.mu.Unlock()

  if r.tokens >= 1.0 {
    return 0
  }

  // 1개의 token이 생성될 때까지 시간 계산
  tokensNeeded := 1.0 - r.tokens
  secondsNeeded := tokensNeeded / r.rate

  return time.Duration(secondsNeeded * float64(time.Second))
}

// min은 두 float64 중 작은 값을 반환합니다.
func min(a, b float64) float64 {
  if a < b {
    return a
  }
  return b
}

// String은 rate limiter의 상태를 문자열로 반환합니다.
func (r *TokenBucketRateLimiter) String() string {
  r.mu.Lock()
  defer r.mu.Unlock()

  return fmt.Sprintf("RateLimiter(rate=%.2f/s, burst=%d, tokens=%.2f)",
    r.rate, r.burst, r.tokens)
}
