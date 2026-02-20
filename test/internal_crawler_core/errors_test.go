package core_test

import (
  "errors"
  "testing"

  "github.com/stretchr/testify/assert"

  core "ecoscrapper/internal/crawler/core"
)

func TestCrawlerError_Error(t *testing.T) {
  err := &core.CrawlerError{
    Category: core.ErrCategoryNetwork,
    Code:     "NET_001",
    Message:  "connection failed",
    URL:      "https://example.com",
    Err:      errors.New("underlying error"),
  }

  expected := "[network:NET_001] connection failed: underlying error"
  assert.Equal(t, expected, err.Error())
}

func TestCrawlerError_Unwrap(t *testing.T) {
  underlyingErr := errors.New("underlying error")
  err := &core.CrawlerError{
    Category: core.ErrCategoryNetwork,
    Code:     "NET_001",
    Message:  "connection failed",
    Err:      underlyingErr,
  }

  assert.Equal(t, underlyingErr, err.Unwrap())
}

func TestCrawlerError_Is(t *testing.T) {
  tests := []struct {
    name     string
    err      *core.CrawlerError
    target   error
    expected bool
  }{
    {
      name: "same code",
      err: &core.CrawlerError{
        Category: core.ErrCategoryNetwork,
        Code:     "NET_001",
      },
      target: &core.CrawlerError{
        Category: core.ErrCategoryNetwork,
        Code:     "NET_001",
      },
      expected: true,
    },
    {
      name: "same category different code",
      err: &core.CrawlerError{
        Category: core.ErrCategoryNetwork,
        Code:     "NET_001",
      },
      target: &core.CrawlerError{
        Category: core.ErrCategoryNetwork,
        Code:     "NET_002",
      },
      expected: true,
    },
    {
      name: "different category",
      err: &core.CrawlerError{
        Category: core.ErrCategoryNetwork,
        Code:     "NET_001",
      },
      target: &core.CrawlerError{
        Category: core.ErrCategoryParse,
        Code:     "PARSE_001",
      },
      expected: false,
    },
    {
      name: "not crawler error",
      err: &core.CrawlerError{
        Category: core.ErrCategoryNetwork,
        Code:     "NET_001",
      },
      target:   errors.New("some error"),
      expected: false,
    },
  }

  for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
      assert.Equal(t, tt.expected, tt.err.Is(tt.target))
    })
  }
}

func TestNewNetworkError(t *testing.T) {
  url := "https://example.com"
  underlyingErr := errors.New("connection refused")

  err := core.NewNetworkError("NET_001", "failed to connect", url, underlyingErr)

  assert.Equal(t, core.ErrCategoryNetwork, err.Category)
  assert.Equal(t, "NET_001", err.Code)
  assert.Equal(t, url, err.URL)
  assert.True(t, err.Retryable)
  assert.Equal(t, underlyingErr, err.Err)
}

func TestNewRateLimitError(t *testing.T) {
  url := "https://example.com"

  err := core.NewRateLimitError("HTTP_429", "rate limited", url, 429)

  assert.Equal(t, core.ErrCategoryRateLimit, err.Category)
  assert.Equal(t, "HTTP_429", err.Code)
  assert.Equal(t, url, err.URL)
  assert.Equal(t, 429, err.StatusCode)
  assert.True(t, err.Retryable)
}

func TestNewParseError(t *testing.T) {
  url := "https://example.com"
  underlyingErr := errors.New("invalid html")

  err := core.NewParseError("PARSE_001", "failed to parse", url, underlyingErr)

  assert.Equal(t, core.ErrCategoryParse, err.Category)
  assert.Equal(t, "PARSE_001", err.Code)
  assert.Equal(t, url, err.URL)
  assert.False(t, err.Retryable)
  assert.Equal(t, underlyingErr, err.Err)
}

func TestNewNotFoundError(t *testing.T) {
  url := "https://example.com"

  err := core.NewNotFoundError(url)

  assert.Equal(t, core.ErrCategoryNotFound, err.Category)
  assert.Equal(t, "HTTP_404", err.Code)
  assert.Equal(t, url, err.URL)
  assert.Equal(t, 404, err.StatusCode)
  assert.False(t, err.Retryable)
}
