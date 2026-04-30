package llm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/llm"
)

func TestStaticCapabilitiesProvider_DefaultLookup(t *testing.T) {
	caps := llm.NewStaticCapabilitiesProvider()

	got, ok := caps.Get("openai", "gpt-4o-mini")
	assert.True(t, ok)
	assert.Equal(t, 0.15, got.CostInputPer1M)
	assert.Equal(t, 0.60, got.CostOutputPer1M)
	assert.Equal(t, 128_000, got.ContextWindow)

	got, ok = caps.Get("anthropic", "claude-opus-4-7")
	assert.True(t, ok)
	assert.Equal(t, 15.0, got.CostInputPer1M)

	_, ok = caps.Get("unknown", "model")
	assert.False(t, ok)
}

func TestStaticCapabilitiesProvider_CustomTable(t *testing.T) {
	custom := map[string]map[string]llm.Capabilities{
		"custom-vendor": {
			"my-model": {CostInputPer1M: 1.0, ContextWindow: 8000},
		},
	}
	caps := llm.NewStaticCapabilitiesProviderFrom(custom)

	got, ok := caps.Get("custom-vendor", "my-model")
	assert.True(t, ok)
	assert.Equal(t, 1.0, got.CostInputPer1M)

	_, ok = caps.Get("custom-vendor", "missing")
	assert.False(t, ok)
}
