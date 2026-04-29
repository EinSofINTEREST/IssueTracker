package llm_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/llm"
)

func TestError_String_WithoutWrapped(t *testing.T) {
	e := &llm.Error{Code: llm.ErrCodeAuth, Provider: "gemini", Message: "missing key"}
	assert.Equal(t, "[llm:gemini:auth] missing key", e.Error())
}

func TestError_String_WithWrapped(t *testing.T) {
	e := &llm.Error{
		Code:     llm.ErrCodeNetwork,
		Provider: "openai",
		Message:  "transport error",
		Err:      errors.New("dial tcp: connection refused"),
	}
	assert.Contains(t, e.Error(), "[llm:openai:network] transport error")
	assert.Contains(t, e.Error(), "dial tcp: connection refused")
}

func TestError_Unwrap_PreservesChain(t *testing.T) {
	root := errors.New("root cause")
	e := &llm.Error{Code: llm.ErrCodeServer, Provider: "anthropic", Err: root}
	assert.True(t, errors.Is(e, root))

	var target *llm.Error
	wrapped := fmt.Errorf("wrap: %w", e)
	assert.True(t, errors.As(wrapped, &target))
	assert.Equal(t, llm.ErrCodeServer, target.Code)
}

func TestIsRetryable_TrueForRetryableLLMError(t *testing.T) {
	e := &llm.Error{Code: llm.ErrCodeRateLimit, Retryable: true}
	assert.True(t, llm.IsRetryable(e))
}

func TestIsRetryable_FalseForNonRetryableLLMError(t *testing.T) {
	e := &llm.Error{Code: llm.ErrCodeAuth, Retryable: false}
	assert.False(t, llm.IsRetryable(e))
}

func TestIsRetryable_FalseForNonLLMError(t *testing.T) {
	assert.False(t, llm.IsRetryable(errors.New("plain")))
	assert.False(t, llm.IsRetryable(nil))
}

func TestIsRetryable_UnwrapsToFindLLMError(t *testing.T) {
	inner := &llm.Error{Code: llm.ErrCodeServer, Retryable: true}
	wrapped := fmt.Errorf("during call: %w", inner)
	assert.True(t, llm.IsRetryable(wrapped))
}
