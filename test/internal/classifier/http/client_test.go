package http_test

import (
  "context"
  "encoding/json"
  "errors"
  "fmt"
  "net/http"
  "net/http/httptest"
  "testing"
  "time"

  "issuetracker/internal/classifier"
  classifierhttp "issuetracker/internal/classifier/http"

  "github.com/stretchr/testify/assert"
  "github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func newTestClient(serverURL string) *classifierhttp.Client {
  return classifierhttp.NewClient(classifierhttp.Config{
    BaseURL: serverURL,
    Timeout: 5 * time.Second,
  })
}

// ─────────────────────────────────────────────────────────────────────────────
// Classify 성공 케이스
// ─────────────────────────────────────────────────────────────────────────────

func TestClient_Classify_성공(t *testing.T) {
  // httptest 서버가 /classify 에 대해 정상 응답을 반환하면 올바른 결과를 파싱해야 합니다.
  want := classifier.ClassifyResult{
    Label:   "technology",
    Reason:  "AI 관련 내용이 포함됩니다",
    ParseOk: true,
  }

  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    assert.Equal(t, http.MethodPost, r.Method)
    assert.Equal(t, "/classify", r.URL.Path)
    assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

    resp := map[string]any{
      "result": map[string]any{
        "label":      want.Label,
        "reason":     want.Reason,
        "parse_ok":   want.ParseOk,
        "raw_output": "",
      },
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(resp)
  }))
  defer server.Close()

  client := newTestClient(server.URL)
  defer client.Close()

  got, err := client.Classify(context.Background(), "AI와 머신러닝 최신 동향", nil)

  require.NoError(t, err)
  require.NotNil(t, got)
  assert.Equal(t, want.Label, got.Result.Label)
  assert.Equal(t, want.Reason, got.Result.Reason)
  assert.Equal(t, want.ParseOk, got.Result.ParseOk)
}

func TestClient_Classify_카테고리_지정_성공(t *testing.T) {
  // categories를 지정했을 때 요청 본문에 포함되고 응답을 올바르게 파싱하는지 검증합니다.
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    var reqBody map[string]any
    require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))

    cats, ok := reqBody["categories"].([]any)
    assert.True(t, ok, "카테고리가 요청 본문에 포함되어야 합니다")
    assert.Len(t, cats, 2)

    resp := map[string]any{
      "result": map[string]any{
        "label":    "sports",
        "reason":   "스포츠 관련",
        "parse_ok": true,
      },
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(resp)
  }))
  defer server.Close()

  client := newTestClient(server.URL)
  defer client.Close()

  categories := []classifier.CategoryInput{
    {Name: "sports", Description: "스포츠 뉴스"},
    {Name: "politics", Description: "정치 뉴스"},
  }
  got, err := client.Classify(context.Background(), "월드컵 경기 결과", categories)

  require.NoError(t, err)
  assert.Equal(t, "sports", got.Result.Label)
}

// ─────────────────────────────────────────────────────────────────────────────
// ClassifyBatch 성공 케이스
// ─────────────────────────────────────────────────────────────────────────────

func TestClient_ClassifyBatch_성공(t *testing.T) {
  // httptest 서버가 /classify/batch 에 대해 정상 응답을 반환하면 모든 항목을 파싱해야 합니다.
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    assert.Equal(t, http.MethodPost, r.Method)
    assert.Equal(t, "/classify/batch", r.URL.Path)

    var reqBody map[string]any
    require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))

    texts, ok := reqBody["texts"].([]any)
    assert.True(t, ok, "texts 필드가 요청 본문에 있어야 합니다")
    assert.Len(t, texts, 2)

    resp := map[string]any{
      "total": 2,
      "results": []map[string]any{
        {
          "index":        0,
          "text_preview": "AI 뉴스...",
          "result": map[string]any{
            "label":    "technology",
            "reason":   "기술 관련",
            "parse_ok": true,
          },
        },
        {
          "index":        1,
          "text_preview": "선거 결과...",
          "result": map[string]any{
            "label":    "politics",
            "reason":   "정치 관련",
            "parse_ok": true,
          },
        },
      },
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(resp)
  }))
  defer server.Close()

  client := newTestClient(server.URL)
  defer client.Close()

  texts := []string{"AI 최신 동향", "선거 결과 발표"}
  got, err := client.ClassifyBatch(context.Background(), texts, nil)

  require.NoError(t, err)
  require.NotNil(t, got)
  assert.Equal(t, 2, got.Total)
  require.Len(t, got.Results, 2)
  assert.Equal(t, 0, got.Results[0].Index)
  assert.Equal(t, "technology", got.Results[0].Result.Label)
  assert.Equal(t, 1, got.Results[1].Index)
  assert.Equal(t, "politics", got.Results[1].Result.Label)
}

// ─────────────────────────────────────────────────────────────────────────────
// 비정상 status code — 에러 메시지에 status/body 포함 여부
// ─────────────────────────────────────────────────────────────────────────────

func TestClient_Classify_비정상_StatusCode_에러에_포함(t *testing.T) {
  // 비-200 응답 시 에러 메시지에 status code와 body가 모두 포함되어야 합니다.
  tests := []struct {
    name       string
    statusCode int
    body       string
  }{
    {"500 Internal Server Error", http.StatusInternalServerError, `{"detail":"서버 내부 오류"}`},
    {"422 Unprocessable Entity", http.StatusUnprocessableEntity, `{"detail":"입력값 오류"}`},
    {"503 Service Unavailable", http.StatusServiceUnavailable, `{"detail":"서비스 불가"}`},
  }

  for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
      server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(tt.statusCode)
        _, _ = w.Write([]byte(tt.body))
      }))
      defer server.Close()

      client := newTestClient(server.URL)
      defer client.Close()

      _, err := client.Classify(context.Background(), "테스트", nil)

      require.Error(t, err)
      errMsg := err.Error()
      // status code 숫자가 에러 메시지에 포함되어야 합니다
      assert.Contains(t, errMsg, fmt.Sprintf("%d", tt.statusCode),
        "에러 메시지에 status code가 없습니다: %s", errMsg)
      // 응답 body가 에러 메시지에 포함되어야 합니다
      assert.Contains(t, errMsg, tt.body,
        "에러 메시지에 응답 body가 없습니다: %s", errMsg)
    })
  }
}

func TestClient_ClassifyBatch_에러_메시지_StatusCode_Body_포함(t *testing.T) {
  // batch endpoint에서도 비-200 응답 시 에러 메시지에 status code와 body가 포함되어야 합니다.
  const errorBody = `{"detail":"배치 크기 초과"}`

  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusBadRequest)
    _, _ = w.Write([]byte(errorBody))
  }))
  defer server.Close()

  client := newTestClient(server.URL)
  defer client.Close()

  _, err := client.ClassifyBatch(context.Background(), []string{"text1"}, nil)

  require.Error(t, err)
  errMsg := err.Error()
  assert.Contains(t, errMsg, "400", "에러 메시지에 status code가 없습니다: %s", errMsg)
  assert.Contains(t, errMsg, errorBody, "에러 메시지에 응답 body가 없습니다: %s", errMsg)
}

// ─────────────────────────────────────────────────────────────────────────────
// timeout / context cancel 동작
// ─────────────────────────────────────────────────────────────────────────────

func TestClient_Classify_Timeout_에러반환(t *testing.T) {
  // 클라이언트 timeout보다 서버 응답이 늦으면 에러를 반환해야 합니다.
  // r.Context().Done()을 기다려 클라이언트 연결이 끊어지면 핸들러 goroutine도 즉시 종료합니다.
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    timer := time.NewTimer(200 * time.Millisecond)
    defer timer.Stop()
    select {
    case <-r.Context().Done():
      return
    case <-timer.C:
      w.WriteHeader(http.StatusOK)
    }
  }))
  defer server.Close()

  client := classifierhttp.NewClient(classifierhttp.Config{
    BaseURL: server.URL,
    Timeout: 50 * time.Millisecond,
  })
  defer client.Close()

  _, err := client.Classify(context.Background(), "테스트", nil)

  require.Error(t, err, "timeout 발생 시 에러를 반환해야 합니다")
  assert.True(t, errors.Is(err, context.DeadlineExceeded), "timeout 에러여야 합니다: %v", err)
}

func TestClient_Classify_ContextCancel_에러반환(t *testing.T) {
  // context가 cancel되면 진행 중인 요청도 중단하고 에러를 반환해야 합니다.
  // r.Context().Done()을 기다려 클라이언트 연결이 끊어지면 핸들러 goroutine도 즉시 종료합니다.
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    timer := time.NewTimer(1 * time.Second)
    defer timer.Stop()
    select {
    case <-r.Context().Done():
      return
    case <-timer.C:
      w.WriteHeader(http.StatusOK)
    }
  }))
  defer server.Close()

  client := newTestClient(server.URL)
  defer client.Close()

  ctx, cancel := context.WithCancel(context.Background())
  cancel() // 즉시 취소

  _, err := client.Classify(ctx, "테스트", nil)

  require.Error(t, err, "context cancel 시 에러를 반환해야 합니다")
  assert.True(t, errors.Is(err, context.Canceled), "context cancel 에러여야 합니다: %v", err)
}

func TestClient_ClassifyBatch_ContextDeadline_에러반환(t *testing.T) {
  // deadline이 지난 context로 batch 요청 시 에러를 반환해야 합니다.
  // r.Context().Done()을 기다려 클라이언트 연결이 끊어지면 핸들러 goroutine도 즉시 종료합니다.
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    timer := time.NewTimer(200 * time.Millisecond)
    defer timer.Stop()
    select {
    case <-r.Context().Done():
      return
    case <-timer.C:
      w.WriteHeader(http.StatusOK)
    }
  }))
  defer server.Close()

  client := newTestClient(server.URL)
  defer client.Close()

  ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
  defer cancel()

  _, err := client.ClassifyBatch(ctx, []string{"text1", "text2"}, nil)

  require.Error(t, err, "deadline 초과 시 에러를 반환해야 합니다")
  assert.True(t, errors.Is(err, context.DeadlineExceeded), "deadline 초과 에러여야 합니다: %v", err)
}
