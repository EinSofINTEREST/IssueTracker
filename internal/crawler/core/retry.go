package core

import (
  "context"
  "errors"
  "math"
  "math/rand"
  "time"

  "ecoscrapper/pkg/logger"
)

// RetryPolicy는 재시도 정책을 정의합니다.
type RetryPolicy struct {
  MaxAttempts     int
  InitialDelay    time.Duration
  MaxDelay        time.Duration
  Multiplier      float64
  Jitter          bool
  RetryableErrors []ErrorCategory
}

// DefaultRetryPolicy는 기본 재시도 정책을 반환합니다.
var DefaultRetryPolicy = RetryPolicy{
  MaxAttempts:  3,
  InitialDelay: 1 * time.Second,
  MaxDelay:     30 * time.Second,
  Multiplier:   2.0,
  Jitter:       true,
  RetryableErrors: []ErrorCategory{
    ErrCategoryNetwork,
    ErrCategoryTimeout,
    ErrCategoryRateLimit,
  },
}

// WithRetry는 함수를 retry policy에 따라 재시도합니다.
// 재시도 가능한 에러가 발생하면 exponential backoff로 재시도합니다.
func WithRetry(ctx context.Context, policy RetryPolicy, fn func() error) error {
  log := logger.FromContext(ctx)
  var lastErr error

  for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
    if attempt > 0 {
      delay := CalculateBackoff(policy, attempt)
      log.WithFields(map[string]interface{}{
        "attempt":    attempt + 1,
        "max":        policy.MaxAttempts,
        "delay_ms":   delay.Milliseconds(),
      }).Warn("retrying after error")

      select {
      case <-ctx.Done():
        return ctx.Err()
      case <-time.After(delay):
      }
    }

    err := fn()
    if err == nil {
      if attempt > 0 {
        log.WithField("attempts", attempt+1).Info("succeeded after retry")
      }
      return nil
    }

    // 재시도 가능한 에러인지 확인
    if !IsRetryable(err, policy) {
      log.WithError(err).WithField("attempt", attempt+1).Debug("error not retryable")
      return err
    }

    lastErr = err
  }

  log.WithError(lastErr).
    WithField("attempts", policy.MaxAttempts).
    Error("max retries exceeded")

  return lastErr
}

// CalculateBackoff는 exponential backoff 값을 계산합니다.
func CalculateBackoff(policy RetryPolicy, attempt int) time.Duration {
  // Exponential backoff: InitialDelay * Multiplier^(attempt-1)
  delay := float64(policy.InitialDelay) * math.Pow(policy.Multiplier, float64(attempt-1))

  // Max delay 제한
  if delay > float64(policy.MaxDelay) {
    delay = float64(policy.MaxDelay)
  }

  // Jitter 적용 (±25%)
  if policy.Jitter {
    jitter := delay * 0.25 * (rand.Float64()*2 - 1)
    delay += jitter
  }

  return time.Duration(delay)
}

// IsRetryable는 에러가 재시도 가능한지 확인합니다.
func IsRetryable(err error, policy RetryPolicy) bool {
  var crawlerErr *CrawlerError
  if errors.As(err, &crawlerErr) {
    if !crawlerErr.Retryable {
      return false
    }

    // Policy에 정의된 카테고리인지 확인
    for _, cat := range policy.RetryableErrors {
      if crawlerErr.Category == cat {
        return true
      }
    }
    return false
  }

  // Context 에러는 재시도하지 않음
  if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
    return false
  }

  // 알 수 없는 에러는 재시도하지 않음
  return false
}
