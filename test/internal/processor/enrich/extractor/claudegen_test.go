package extractor_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/enrich/extractor"
)

// stubLoader 는 항상 같은 template 을 반환하는 prompt.Loader stub.
type stubLoader struct {
	tpl string
}

func (s *stubLoader) Load(_ string) (string, error) { return s.tpl, nil }

// stubRunner 는 미리 정의한 stdout 또는 error 를 반환하는 SessionRunner stub.
type stubRunner struct {
	stdout string
	err    error
	calls  int
}

func (s *stubRunner) RunEnrichSession(
	_ context.Context,
	_ string,
	_ map[string][]byte,
	_ string,
) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.stdout, nil
}

func TestClaudegenExtractor_Success(t *testing.T) {
	stdout := `{
		"entities": [{"type": "person", "name": "Alice", "mentions": 2}],
		"claims":   [{"text": "Alice did X", "subject": "Alice", "predicate": "did", "object": "X"}],
		"facts":    [{"key": "casualties", "value": "32", "unit": "people"}],
		"topics":   ["politics", "society"],
		"sentiment": "neutral"
	}`

	ex, err := extractor.NewClaudegenExtractor(
		&stubRunner{stdout: stdout},
		&stubLoader{tpl: "page at {{HOST}}"},
	)
	require.NoError(t, err)

	got, err := ex.Extract(context.Background(), extractor.Input{
		URL:   "https://example.com/a",
		Host:  "example.com",
		Title: "Hi",
		HTML:  "<html/>",
	})
	require.NoError(t, err)

	assert.Len(t, got.Entities, 1)
	assert.Equal(t, "Alice", got.Entities[0].Name)
	assert.Equal(t, extractor.EntityTypePerson, got.Entities[0].Type)
	assert.Len(t, got.Claims, 1)
	assert.Len(t, got.Facts, 1)
	assert.Equal(t, "32", got.Facts[0].Value)
	assert.Equal(t, []string{"politics", "society"}, got.Topics)
	assert.Equal(t, extractor.SentimentNeutral, got.Sentiment)
}

func TestClaudegenExtractor_StripsMarkdownFence(t *testing.T) {
	stdout := "```json\n" + `{"entities":[],"claims":[],"facts":[],"topics":[],"sentiment":"positive"}` + "\n```"

	ex, err := extractor.NewClaudegenExtractor(
		&stubRunner{stdout: stdout},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	got, err := ex.Extract(context.Background(), extractor.Input{})
	require.NoError(t, err)
	assert.Equal(t, extractor.SentimentPositive, got.Sentiment)
}

func TestClaudegenExtractor_NilFieldsNormalized(t *testing.T) {
	// 응답에 entities / claims / facts / topics 가 모두 누락 — empty slice 로 정규화.
	stdout := `{"sentiment": "neutral"}`

	ex, err := extractor.NewClaudegenExtractor(
		&stubRunner{stdout: stdout},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	got, err := ex.Extract(context.Background(), extractor.Input{})
	require.NoError(t, err)
	assert.NotNil(t, got.Entities)
	assert.NotNil(t, got.Claims)
	assert.NotNil(t, got.Facts)
	assert.NotNil(t, got.Topics)
	assert.Empty(t, got.Entities)
	assert.Empty(t, got.Claims)
	assert.Empty(t, got.Facts)
	assert.Empty(t, got.Topics)
}

func TestClaudegenExtractor_MalformedOutput_ReturnsError(t *testing.T) {
	ex, err := extractor.NewClaudegenExtractor(
		&stubRunner{stdout: "not json"},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	_, err = ex.Extract(context.Background(), extractor.Input{})
	assert.Error(t, err)
}

func TestClaudegenExtractor_SessionError_Propagates(t *testing.T) {
	ex, err := extractor.NewClaudegenExtractor(
		&stubRunner{err: errors.New("docker exec failed")},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	_, err = ex.Extract(context.Background(), extractor.Input{})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "session")
}

func TestNoopExtractor_ReturnsEmpty(t *testing.T) {
	got, err := extractor.NewNoopExtractor().Extract(context.Background(), extractor.Input{})
	require.NoError(t, err)
	assert.Empty(t, got.Entities)
	assert.Empty(t, got.Claims)
	assert.Empty(t, got.Facts)
	assert.Empty(t, got.Topics)
	assert.Equal(t, extractor.SentimentNeutral, got.Sentiment)
}

func TestNewClaudegenExtractor_NilArgs(t *testing.T) {
	_, err := extractor.NewClaudegenExtractor(nil, &stubLoader{tpl: "t"})
	assert.Error(t, err)

	_, err = extractor.NewClaudegenExtractor(&stubRunner{stdout: "{}"}, nil)
	assert.Error(t, err)
}
