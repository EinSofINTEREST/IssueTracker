package openai_test

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
	"issuetracker/pkg/llm/openai"
)

const successResponse = `{
  "id":"chatcmpl-1",
  "model":"gpt-4o-mini-2024-07-18",
  "choices":[{
    "message":{"role":"assistant","content":"Hello there"},
    "finish_reason":"stop"
  }],
  "usage":{"prompt_tokens":15,"completion_tokens":4}
}`

func TestOpenAI_Generate_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		assert.Equal(t, "/chat/completions", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		// system / user / assistant 모두 messages 배열에 그대로
		msgs := req["messages"].([]any)
		assert.Len(t, msgs, 3)
		assert.Equal(t, "system", msgs[0].(map[string]any)["role"])
		assert.Equal(t, "user", msgs[1].(map[string]any)["role"])
		assert.Equal(t, "assistant", msgs[2].(map[string]any)["role"])
		assert.Equal(t, "gpt-4o-mini", req["model"])

		w.WriteHeader(200)
		_, _ = w.Write([]byte(successResponse))
	}))
	defer srv.Close()

	p := openai.New("sk-test", openai.WithBaseURL(srv.URL))
	resp, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "Be brief."},
			{Role: llm.RoleUser, Content: "Hi"},
			{Role: llm.RoleAssistant, Content: "Hi back"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello there", resp.Content)
	assert.Equal(t, "gpt-4o-mini-2024-07-18", resp.Model)
	assert.Equal(t, 15, resp.InputTokens)
	assert.Equal(t, 4, resp.OutputTokens)
	assert.Equal(t, "stop", resp.StopReason)
}

func TestOpenAI_Generate_NoAPIKey_Auth(t *testing.T) {
	p := openai.New("")
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, "openai", lerr.Provider)
	assert.Equal(t, llm.ErrCodeAuth, lerr.Code)
}

func TestOpenAI_Generate_EmptyChoices_Unknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	p := openai.New("k", openai.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeUnknown, lerr.Code)
}

func TestOpenAI_Generate_HTTP500_Server_Retryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("server overload"))
	}))
	defer srv.Close()

	p := openai.New("k", openai.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	assert.True(t, llm.IsRetryable(err))
}

func TestOpenAI_Generate_TemperatureAndMaxTokens_Forwarded(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(successResponse))
	}))
	defer srv.Close()

	p := openai.New("k", openai.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "x"}},
		Temperature: 0.7,
		MaxTokens:   200,
	})
	require.NoError(t, err)
	assert.InDelta(t, 0.7, seen["temperature"], 0.001)
	assert.EqualValues(t, 200, seen["max_tokens"])
}

func TestOpenAI_Name(t *testing.T) {
	assert.Equal(t, "openai", openai.New("").Name())
}
