package gemini_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/gemini"
)

// gemini API 응답 mock — usageMetadata + candidates 1개 (text part 1개).
const successResponse = `{
  "candidates": [{
    "content": {"role":"model","parts":[{"text":"Hello, world!"}]},
    "finishReason": "STOP"
  }],
  "usageMetadata": {"promptTokenCount":12,"candidatesTokenCount":3},
  "modelVersion": "gemini-2.5-flash-001"
}`

func TestGemini_Generate_Success_NormalizesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Gemini 는 query param 으로 API key 전달
		assert.Equal(t, "test-api-key", r.URL.Query().Get("key"))
		// path 에 model 포함
		assert.Contains(t, r.URL.Path, "/models/gemini-2.5-flash:generateContent")
		// 요청 body 검사 — system 분리, role 매핑
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		require.NoError(t, json.Unmarshal(body, &req))
		assert.NotNil(t, req["systemInstruction"])
		contents, _ := req["contents"].([]any)
		assert.Len(t, contents, 1)
		c0 := contents[0].(map[string]any)
		assert.Equal(t, "user", c0["role"])

		w.WriteHeader(200)
		_, _ = w.Write([]byte(successResponse))
	}))
	defer srv.Close()

	p := gemini.New("test-api-key", gemini.WithBaseURL(srv.URL))
	resp, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "You are a helpful assistant."},
			{Role: llm.RoleUser, Content: "Hi"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello, world!", resp.Content)
	assert.Equal(t, "gemini-2.5-flash-001", resp.Model)
	assert.Equal(t, 12, resp.InputTokens)
	assert.Equal(t, 3, resp.OutputTokens)
	assert.Equal(t, "STOP", resp.StopReason)
}

func TestGemini_Generate_AssistantRoleMapsToModel(t *testing.T) {
	var seenContents []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		for _, c := range req["contents"].([]any) {
			seenContents = append(seenContents, c.(map[string]any))
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(successResponse))
	}))
	defer srv.Close()

	p := gemini.New("k", gemini.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Q1"},
			{Role: llm.RoleAssistant, Content: "A1"},
			{Role: llm.RoleUser, Content: "Q2"},
		},
	})
	require.NoError(t, err)
	require.Len(t, seenContents, 3)
	assert.Equal(t, "user", seenContents[0]["role"])
	assert.Equal(t, "model", seenContents[1]["role"], "assistant 는 Gemini 의 'model' 로 매핑")
	assert.Equal(t, "user", seenContents[2]["role"])
}

func TestGemini_Generate_NoAPIKey_ReturnsAuthError(t *testing.T) {
	p := gemini.New("")
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	assertLLMError(t, err, "gemini", llm.ErrCodeAuth)
}

func TestGemini_Generate_EmptyMessages_BadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when request is invalid")
	}))
	defer srv.Close()

	p := gemini.New("k", gemini.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{})
	require.Error(t, err)
	assertLLMError(t, err, "gemini", llm.ErrCodeBadRequest)
}

func TestGemini_Generate_EmptyCandidates_Unknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"candidates":[]}`))
	}))
	defer srv.Close()

	p := gemini.New("k", gemini.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	assertLLMError(t, err, "gemini", llm.ErrCodeUnknown)
}

func TestGemini_Generate_HTTP429_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"quota exceeded"}`))
	}))
	defer srv.Close()

	p := gemini.New("k", gemini.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	assertLLMError(t, err, "gemini", llm.ErrCodeRateLimit)
	assert.True(t, llm.IsRetryable(err))
}

func TestGemini_Name_ReturnsGemini(t *testing.T) {
	assert.Equal(t, "gemini", gemini.New("").Name())
}

// assertLLMError 는 모든 provider 테스트에서 공유하는 헬퍼 — *llm.Error 검증.
func assertLLMError(t *testing.T, err error, wantProvider string, wantCode llm.ErrorCode) {
	t.Helper()
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, wantProvider, lerr.Provider)
	assert.Equal(t, wantCode, lerr.Code)
}
