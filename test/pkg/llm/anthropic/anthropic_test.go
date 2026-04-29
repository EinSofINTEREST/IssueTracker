package anthropic_test

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
	"issuetracker/pkg/llm/anthropic"
)

const successResponse = `{
  "id":"msg_01",
  "model":"claude-opus-4-7",
  "content":[
    {"type":"text","text":"Hello"},
    {"type":"text","text":", world"}
  ],
  "stop_reason":"end_turn",
  "usage":{"input_tokens":20,"output_tokens":5}
}`

func TestAnthropic_Generate_Success_ConcatTextBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
		assert.Equal(t, "/messages", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		// system 은 별도 top-level 필드
		assert.Equal(t, "Be brief.", req["system"])
		// messages 에는 user/assistant 만
		msgs := req["messages"].([]any)
		assert.Len(t, msgs, 2)
		assert.Equal(t, "user", msgs[0].(map[string]any)["role"])
		assert.Equal(t, "assistant", msgs[1].(map[string]any)["role"])
		// max_tokens 필수 — 미지정 시 default 4096
		assert.EqualValues(t, 4096, req["max_tokens"])

		w.WriteHeader(200)
		_, _ = w.Write([]byte(successResponse))
	}))
	defer srv.Close()

	p := anthropic.New("test-key", anthropic.WithBaseURL(srv.URL))
	resp, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "Be brief."},
			{Role: llm.RoleUser, Content: "Hi"},
			{Role: llm.RoleAssistant, Content: "Hi back"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello, world", resp.Content, "다중 text 블록은 합쳐서 반환")
	assert.Equal(t, "claude-opus-4-7", resp.Model)
	assert.Equal(t, 20, resp.InputTokens)
	assert.Equal(t, 5, resp.OutputTokens)
	assert.Equal(t, "end_turn", resp.StopReason)
}

func TestAnthropic_Generate_MultipleSystem_Concatenated(t *testing.T) {
	var seenSystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		seenSystem = req["system"].(string)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(successResponse))
	}))
	defer srv.Close()

	p := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "First instruction."},
			{Role: llm.RoleSystem, Content: "Second instruction."},
			{Role: llm.RoleUser, Content: "x"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "First instruction.\n\nSecond instruction.", seenSystem)
}

func TestAnthropic_Generate_MaxTokensOverride(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(successResponse))
	}))
	defer srv.Close()

	p := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: "x"}},
		MaxTokens: 1000,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1000, seen["max_tokens"])
}

func TestAnthropic_Generate_NoAPIKey_Auth(t *testing.T) {
	p := anthropic.New("")
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, "anthropic", lerr.Provider)
	assert.Equal(t, llm.ErrCodeAuth, lerr.Code)
}

func TestAnthropic_Generate_HTTP400_ContextLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"prompt exceeds maximum context length"}`))
	}))
	defer srv.Close()

	p := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeContextLimit, lerr.Code)
	assert.False(t, llm.IsRetryable(err))
}

func TestAnthropic_Generate_EmptyContent_Unknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"content":[]}`))
	}))
	defer srv.Close()

	p := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	_, err := p.Generate(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeUnknown, lerr.Code)
}

func TestAnthropic_Name(t *testing.T) {
	assert.Equal(t, "anthropic", anthropic.New("").Name())
}
