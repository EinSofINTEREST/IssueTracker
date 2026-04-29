package llm_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm"
	_ "issuetracker/pkg/llm/providers"
)

func TestNew_KnownProviders_ReturnNonNil(t *testing.T) {
	tests := []struct {
		name     string
		provider string
	}{
		{"gemini lowercase", "gemini"},
		{"gemini uppercase", "GEMINI"},
		{"openai", "openai"},
		{"anthropic", "anthropic"},
		{"claude alias", "claude"},
		{"with whitespace", "  openai  "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := llm.New(llm.Config{Provider: tc.provider, APIKey: "dummy"})
			require.NoError(t, err)
			assert.NotNil(t, p)
		})
	}
}

func TestNew_EmptyProvider_ReturnsBadRequest(t *testing.T) {
	_, err := llm.New(llm.Config{APIKey: "x"})
	require.Error(t, err)
	var lerr *llm.Error
	require.True(t, errors.As(err, &lerr))
	assert.Equal(t, llm.ErrCodeBadRequest, lerr.Code)
}

func TestNew_UnknownProvider_ReturnsBadRequestWithRegisteredList(t *testing.T) {
	_, err := llm.New(llm.Config{Provider: "no-such-provider", APIKey: "x"})
	require.Error(t, err)
	var lerr *llm.Error
	require.True(t, errors.As(err, &lerr))
	assert.Equal(t, llm.ErrCodeBadRequest, lerr.Code)
	// 디버깅 친화: 등록된 이름이 메시지에 포함됨
	assert.Contains(t, lerr.Message, "registered:")
}

func TestNew_ProviderNameAsExpected(t *testing.T) {
	for name, want := range map[string]string{
		"gemini":    "gemini",
		"openai":    "openai",
		"anthropic": "anthropic",
		"claude":    "anthropic", // 별칭이라도 Provider.Name() 은 raw provider 이름
	} {
		p, err := llm.New(llm.Config{Provider: name, APIKey: "x"})
		require.NoError(t, err)
		assert.Equal(t, want, p.Name(), "config provider %q", name)
	}
}
