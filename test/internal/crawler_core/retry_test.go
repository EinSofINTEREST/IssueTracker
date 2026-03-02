package core_test

import (
  core "issuetracker/internal/crawler/core"

  "context"
  "errors"
  "testing"
  "time"

  "github.com/stretchr/testify/assert"
)

func TestWithRetry_Success(t *testing.T) {
  policy := core.RetryPolicy{
    MaxAttempts:  3,
    InitialDelay: 10 * time.Millisecond,
    MaxDelay:     100 * time.Millisecond,
    Multiplier:   2.0,
    Jitter:       false,
  }

  attempts := 0
  fn := func() error {
    attempts++
    return nil
  }

  err := core.WithRetry(context.Background(), policy, fn)

  assert.NoError(t, err)
  assert.Equal(t, 1, attempts)
}

func TestWithRetry_SuccessAfterRetries(t *testing.T) {
  policy := core.RetryPolicy{
    MaxAttempts:  3,
    InitialDelay: 10 * time.Millisecond,
    MaxDelay:     100 * time.Millisecond,
    Multiplier:   2.0,
    Jitter:       false,
    RetryableErrors: []core.ErrorCategory{
      core.ErrCategoryNetwork,
    },
  }

  attempts := 0
  fn := func() error {
    attempts++
    if attempts < 3 {
      return core.NewNetworkError("NET_001", "temporary error", "http://example.com", errors.New("test"))
    }
    return nil
  }

  err := core.WithRetry(context.Background(), policy, fn)

  assert.NoError(t, err)
  assert.Equal(t, 3, attempts)
}

func TestWithRetry_MaxAttemptsExceeded(t *testing.T) {
  policy := core.RetryPolicy{
    MaxAttempts:  3,
    InitialDelay: 10 * time.Millisecond,
    MaxDelay:     100 * time.Millisecond,
    Multiplier:   2.0,
    Jitter:       false,
    RetryableErrors: []core.ErrorCategory{
      core.ErrCategoryNetwork,
    },
  }

  attempts := 0
  testErr := core.NewNetworkError("NET_001", "persistent error", "http://example.com", errors.New("test"))

  fn := func() error {
    attempts++
    return testErr
  }

  err := core.WithRetry(context.Background(), policy, fn)

  assert.Error(t, err)
  assert.Equal(t, 3, attempts)
  assert.ErrorIs(t, err, testErr)
}

func TestWithRetry_NonRetryableError(t *testing.T) {
  policy := core.RetryPolicy{
    MaxAttempts:  3,
    InitialDelay: 10 * time.Millisecond,
    MaxDelay:     100 * time.Millisecond,
    Multiplier:   2.0,
    Jitter:       false,
    RetryableErrors: []core.ErrorCategory{
      core.ErrCategoryNetwork,
    },
  }

  attempts := 0
  testErr := core.NewParseError("PARSE_001", "parse failed", "http://example.com", errors.New("test"))

  fn := func() error {
    attempts++
    return testErr
  }

  err := core.WithRetry(context.Background(), policy, fn)

  // Non-retryable 에러는 즉시 반환
  assert.Error(t, err)
  assert.Equal(t, 1, attempts)
  assert.ErrorIs(t, err, testErr)
}

func TestWithRetry_ContextCanceled(t *testing.T) {
  policy := core.RetryPolicy{
    MaxAttempts:  5,
    InitialDelay: 100 * time.Millisecond,
    MaxDelay:     1 * time.Second,
    Multiplier:   2.0,
    Jitter:       false,
    RetryableErrors: []core.ErrorCategory{
      core.ErrCategoryNetwork,
    },
  }

  ctx, cancel := context.WithCancel(context.Background())

  attempts := 0
  fn := func() error {
    attempts++
    if attempts == 2 {
      cancel() // 두 번째 시도 후 취소
    }
    return core.NewNetworkError("NET_001", "error", "http://example.com", errors.New("test"))
  }

  err := core.WithRetry(ctx, policy, fn)

  // Context canceled로 종료
  assert.ErrorIs(t, err, context.Canceled)
  assert.LessOrEqual(t, attempts, 3) // 취소 전후로 최대 3번
}

func TestCalculateBackoff(t *testing.T) {
  policy := core.RetryPolicy{
    InitialDelay: 100 * time.Millisecond,
    MaxDelay:     1 * time.Second,
    Multiplier:   2.0,
    Jitter:       false,
  }

  tests := []struct {
    name     string
    attempt  int
    expected time.Duration
  }{
    {
      name:     "first retry",
      attempt:  1,
      expected: 100 * time.Millisecond,
    },
    {
      name:     "second retry",
      attempt:  2,
      expected: 200 * time.Millisecond,
    },
    {
      name:     "third retry",
      attempt:  3,
      expected: 400 * time.Millisecond,
    },
    {
      name:     "capped at max",
      attempt:  10,
      expected: 1 * time.Second,
    },
  }

  for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
      delay := core.CalculateBackoff(policy, tt.attempt)
      assert.Equal(t, tt.expected, delay)
    })
  }
}

func TestCalculateBackoff_WithJitter(t *testing.T) {
  policy := core.RetryPolicy{
    InitialDelay: 100 * time.Millisecond,
    MaxDelay:     1 * time.Second,
    Multiplier:   2.0,
    Jitter:       true,
  }

  delay := core.CalculateBackoff(policy, 1)

  // Jitter는 ±25%이므로 75ms ~ 125ms 범위
  assert.GreaterOrEqual(t, delay, 75*time.Millisecond)
  assert.LessOrEqual(t, delay, 125*time.Millisecond)
}

func TestIsRetryable(t *testing.T) {
  policy := core.RetryPolicy{
    RetryableErrors: []core.ErrorCategory{
      core.ErrCategoryNetwork,
      core.ErrCategoryTimeout,
    },
  }

  tests := []struct {
    name     string
    err      error
    expected bool
  }{
    {
      name: "retryable network error",
      err: core.NewNetworkError("NET_001", "error", "http://example.com", errors.New("test")),
      expected: true,
    },
    {
      name: "non-retryable parse error",
      err: core.NewParseError("PARSE_001", "error", "http://example.com", errors.New("test")),
      expected: false,
    },
    {
      name: "rate limit not in policy",
      err: core.NewRateLimitError("HTTP_429", "error", "http://example.com", 429),
      expected: false,
    },
    {
      name:     "context canceled",
      err:      context.Canceled,
      expected: false,
    },
    {
      name:     "context deadline exceeded",
      err:      context.DeadlineExceeded,
      expected: false,
    },
    {
      name:     "unknown error",
      err:      errors.New("unknown"),
      expected: false,
    },
  }

  for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
      result := core.IsRetryable(tt.err, policy)
      assert.Equal(t, tt.expected, result)
    })
  }
}

func TestDefaultRetryPolicy(t *testing.T) {
  assert.Equal(t, 3, core.DefaultRetryPolicy.MaxAttempts)
  assert.Equal(t, 1*time.Second, core.DefaultRetryPolicy.InitialDelay)
  assert.Equal(t, 30*time.Second, core.DefaultRetryPolicy.MaxDelay)
  assert.Equal(t, 2.0, core.DefaultRetryPolicy.Multiplier)
  assert.True(t, core.DefaultRetryPolicy.Jitter)
  assert.Contains(t, core.DefaultRetryPolicy.RetryableErrors, core.ErrCategoryNetwork)
  assert.Contains(t, core.DefaultRetryPolicy.RetryableErrors, core.ErrCategoryTimeout)
  assert.Contains(t, core.DefaultRetryPolicy.RetryableErrors, core.ErrCategoryRateLimit)
}
