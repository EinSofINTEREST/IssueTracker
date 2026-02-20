package core_test

import (
  core "ecoscrapper/internal/crawler/core"

  "context"
  "net/http"
  "net/http/httptest"
  "strings"
  "testing"
  "time"

  "github.com/stretchr/testify/assert"
  "github.com/stretchr/testify/require"
)

func TestNewHTTPClient(t *testing.T) {
  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  assert.NotNil(t, client)
}

func TestHTTPClient_Get_Success(t *testing.T) {
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    assert.Equal(t, http.MethodGet, r.Method)
    assert.NotEmpty(t, r.Header.Get("User-Agent"))

    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte("test response"))
  }))
  defer server.Close()

  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  resp, err := client.Get(context.Background(), server.URL)

  require.NoError(t, err)
  assert.Equal(t, http.StatusOK, resp.StatusCode)
  assert.Equal(t, "test response", string(resp.Body))
}

func TestHTTPClient_Get_NotFound(t *testing.T) {
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusNotFound)
  }))
  defer server.Close()

  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  resp, err := client.Get(context.Background(), server.URL)

  assert.Nil(t, resp)
  require.Error(t, err)

  var crawlerErr *core.CrawlerError
  assert.ErrorAs(t, err, &crawlerErr)
  assert.Equal(t, core.ErrCategoryNotFound, crawlerErr.Category)
  assert.Equal(t, "HTTP_404", crawlerErr.Code)
}

func TestHTTPClient_Get_RateLimited(t *testing.T) {
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusTooManyRequests)
  }))
  defer server.Close()

  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  resp, err := client.Get(context.Background(), server.URL)

  assert.Nil(t, resp)
  require.Error(t, err)

  var crawlerErr *core.CrawlerError
  assert.ErrorAs(t, err, &crawlerErr)
  assert.Equal(t, core.ErrCategoryRateLimit, crawlerErr.Category)
  assert.Equal(t, "HTTP_429", crawlerErr.Code)
  assert.True(t, crawlerErr.Retryable)
}

func TestHTTPClient_Get_ServerError(t *testing.T) {
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusInternalServerError)
  }))
  defer server.Close()

  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  resp, err := client.Get(context.Background(), server.URL)

  assert.Nil(t, resp)
  require.Error(t, err)

  var crawlerErr *core.CrawlerError
  assert.ErrorAs(t, err, &crawlerErr)
  assert.Equal(t, core.ErrCategoryNetwork, crawlerErr.Category)
  assert.True(t, crawlerErr.Retryable)
}

func TestHTTPClient_Get_Timeout(t *testing.T) {
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    time.Sleep(2 * time.Second)
    w.WriteHeader(http.StatusOK)
  }))
  defer server.Close()

  config := core.DefaultConfig()
  config.Timeout = 100 * time.Millisecond
  client := core.NewHTTPClient(config)

  resp, err := client.Get(context.Background(), server.URL)

  assert.Nil(t, resp)
  require.Error(t, err)

  var crawlerErr *core.CrawlerError
  assert.ErrorAs(t, err, &crawlerErr)
  assert.Equal(t, core.ErrCategoryNetwork, crawlerErr.Category)
}

func TestHTTPClient_Get_ContextCanceled(t *testing.T) {
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    time.Sleep(1 * time.Second)
    w.WriteHeader(http.StatusOK)
  }))
  defer server.Close()

  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  ctx, cancel := context.WithCancel(context.Background())
  cancel() // 즉시 취소

  resp, err := client.Get(ctx, server.URL)

  assert.Nil(t, resp)
  require.Error(t, err)
}

func TestHTTPClient_Get_LargeResponse(t *testing.T) {
  largeBody := strings.Repeat("a", core.MaxResponseBodySize+1000)

  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte(largeBody))
  }))
  defer server.Close()

  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  resp, err := client.Get(context.Background(), server.URL)

  // Body는 MaxResponseBodySize로 제한됨
  require.NoError(t, err)
  assert.LessOrEqual(t, len(resp.Body), core.MaxResponseBodySize)
}

func TestHTTPClient_Post_Success(t *testing.T) {
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    assert.Equal(t, http.MethodPost, r.Method)
    assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte(`{"status":"ok"}`))
  }))
  defer server.Close()

  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  body := []byte(`{"key":"value"}`)
  resp, err := client.Post(context.Background(), server.URL, body)

  require.NoError(t, err)
  assert.Equal(t, http.StatusOK, resp.StatusCode)
  assert.Contains(t, string(resp.Body), "ok")
}

func TestHTTPClient_Headers(t *testing.T) {
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // User-Agent 확인
    assert.NotEmpty(t, r.Header.Get("User-Agent"))
    assert.Contains(t, r.Header.Get("User-Agent"), "EcoScrapper")

    // Accept-Encoding 확인
    assert.NotEmpty(t, r.Header.Get("Accept-Encoding"))

    w.WriteHeader(http.StatusOK)
  }))
  defer server.Close()

  config := core.DefaultConfig()
  client := core.NewHTTPClient(config)

  _, err := client.Get(context.Background(), server.URL)
  require.NoError(t, err)
}
