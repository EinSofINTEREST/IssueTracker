package rule_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fetcherRule "issuetracker/internal/processor/fetcher/rule"
)

// TestForceFetcherToken_InitAndValidate:
// process-local token 초기화 후 ValidateForceFetcherToken 가 일치 시 true.
func TestForceFetcherToken_InitAndValidate(t *testing.T) {
	require.NoError(t, fetcherRule.InitForceFetcherToken())

	tok := fetcherRule.ForceFetcherTokenValue()
	assert.NotEmpty(t, tok)
	assert.True(t, fetcherRule.ValidateForceFetcherToken(tok))
	assert.False(t, fetcherRule.ValidateForceFetcherToken(""))
	assert.False(t, fetcherRule.ValidateForceFetcherToken("wrong-token"))
}
