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

// TestClaudegenExtractor_StripsFenceVariants — gemini-review PR #452 반영.
// stripFences 가 다양한 LLM 응답 변형 (preamble / 언어 ID 무 newline / trailing 텍스트) 에서
// 견고하게 동작하는지 검증.
func TestClaudegenExtractor_StripsFenceVariants(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
	}{
		{
			name:   "preamble before fence",
			stdout: "Here is the JSON:\n```json\n{\"sentiment\":\"positive\"}\n```",
		},
		{
			name:   "language id without newline",
			stdout: "```json{\"sentiment\":\"positive\"}```",
		},
		{
			name:   "trailing text after closing fence",
			stdout: "```json\n{\"sentiment\":\"positive\"}\n```\nThanks for asking!",
		},
		{
			name:   "no fence at all",
			stdout: `{"sentiment":"positive"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ex, err := extractor.NewClaudegenExtractor(
				&stubRunner{stdout: c.stdout},
				&stubLoader{tpl: "t"},
			)
			require.NoError(t, err)
			got, err := ex.Extract(context.Background(), extractor.Input{})
			require.NoError(t, err)
			assert.Equal(t, extractor.SentimentPositive, got.Sentiment, "case %q", c.name)
		})
	}
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

// ─────────────────────────────────────────────────────────────────────────────
// Verifier (이슈 #448)
// ─────────────────────────────────────────────────────────────────────────────

func TestClaudegenVerifier_Success(t *testing.T) {
	stdout := `{"verifications":[
		{"claim_idx": 0, "verdict": "supported", "sources": ["https://news.example.com/a"], "note": "matches AP"},
		{"claim_idx": 1, "verdict": "unverified"}
	]}`
	runner := &stubRunner{stdout: stdout}
	v, err := extractor.NewClaudegenVerifier(runner, &stubLoader{tpl: "verify {{CLAIMS_JSON}} {{CANDIDATES_JSON}}"})
	require.NoError(t, err)

	got, err := v.Verify(context.Background(), extractor.VerifyInput{
		URL:    "https://example.com/p",
		Host:   "example.com",
		Title:  "T",
		Claims: []extractor.Claim{{Text: "c0"}, {Text: "c1"}},
		Candidates: []extractor.CandidateRef{
			{URL: "https://other.com/x", Title: "other"},
		},
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, 0, got[0].ClaimIdx)
	assert.Equal(t, "supported", got[0].Verdict)
	assert.Equal(t, "unverified", got[1].Verdict)
	assert.Equal(t, 1, runner.calls)
}

func TestClaudegenVerifier_UnknownVerdict_FallsBackToUnverified(t *testing.T) {
	stdout := `{"verifications":[
		{"claim_idx": 0, "verdict": "maybe"}
	]}`
	v, err := extractor.NewClaudegenVerifier(
		&stubRunner{stdout: stdout},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	got, err := v.Verify(context.Background(), extractor.VerifyInput{
		Claims: []extractor.Claim{{Text: "c"}},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "unverified", got[0].Verdict)
}

func TestClaudegenVerifier_EmptyClaims_SkipsRunner(t *testing.T) {
	runner := &stubRunner{stdout: "this should never be parsed"}
	v, err := extractor.NewClaudegenVerifier(
		runner,
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	got, err := v.Verify(context.Background(), extractor.VerifyInput{Claims: nil})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, 0, runner.calls, "runner must not be called when claims empty")
}

func TestClaudegenVerifier_SessionError_Propagates(t *testing.T) {
	v, err := extractor.NewClaudegenVerifier(
		&stubRunner{err: errors.New("exec failure")},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	_, err = v.Verify(context.Background(), extractor.VerifyInput{
		Claims: []extractor.Claim{{Text: "c"}},
	})
	require.Error(t, err)
}

func TestNoopVerifier_ReturnsEmpty(t *testing.T) {
	got, err := extractor.NewNoopVerifier().Verify(context.Background(), extractor.VerifyInput{
		Claims: []extractor.Claim{{Text: "c"}},
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestNewClaudegenVerifier_NilArgs(t *testing.T) {
	_, err := extractor.NewClaudegenVerifier(nil, &stubLoader{tpl: "t"})
	assert.Error(t, err)
	_, err = extractor.NewClaudegenVerifier(&stubRunner{stdout: "{}"}, nil)
	assert.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Contextualizer (이슈 #449)
// ─────────────────────────────────────────────────────────────────────────────

func TestClaudegenContextualizer_Success(t *testing.T) {
	stdout := `{
		"background": [
			{"subject": "Acme Corp", "category": "org", "summary": "Founded in 1980 — global manufacturer.", "sources": ["https://en.wikipedia.org/wiki/Acme"]}
		],
		"timeline": [
			{"date": "2025-01-15", "event": "Quarterly results announced", "source": "https://news.example.org/q"}
		],
		"implications": {
			"political": "",
			"social": "Workforce concerns in two regions.",
			"technical": ""
		}
	}`
	runner := &stubRunner{stdout: stdout}
	c, err := extractor.NewClaudegenContextualizer(runner, &stubLoader{tpl: "ctx {{ENTITIES_JSON}} {{CLAIMS_JSON}}"})
	require.NoError(t, err)

	got, err := c.Provide(context.Background(), extractor.ContextInput{
		URL:      "https://example.com/p",
		Host:     "example.com",
		Title:    "T",
		Entities: []extractor.Entity{{Type: extractor.EntityTypeOrg, Name: "Acme"}},
		Claims:   []extractor.Claim{{Text: "Acme announced X"}},
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Len(t, got.Background, 1)
	assert.Equal(t, "Acme Corp", got.Background[0].Subject)
	assert.Len(t, got.Timeline, 1)
	require.NotNil(t, got.Implications)
	assert.Equal(t, "Workforce concerns in two regions.", got.Implications.Social)
	assert.Equal(t, 1, runner.calls)
}

func TestClaudegenContextualizer_EmptyImplications_NormalizedToNil(t *testing.T) {
	// 모든 implications 필드가 빈 문자열이면 Implications 자체가 nil 로 정규화.
	stdout := `{
		"background": [],
		"timeline": [],
		"implications": {"political": "", "social": "", "technical": ""}
	}`
	c, err := extractor.NewClaudegenContextualizer(
		&stubRunner{stdout: stdout},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	got, err := c.Provide(context.Background(), extractor.ContextInput{
		Entities: []extractor.Entity{{Name: "X"}},
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.Implications)
	assert.True(t, got.IsEmpty())
}

func TestClaudegenContextualizer_NoEntitiesOrClaims_SkipsRunner(t *testing.T) {
	runner := &stubRunner{stdout: "should not be parsed"}
	c, err := extractor.NewClaudegenContextualizer(
		runner,
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)

	got, err := c.Provide(context.Background(), extractor.ContextInput{})
	require.NoError(t, err)
	assert.Nil(t, got, "no entities/claims → runner skip, nil context")
	assert.Equal(t, 0, runner.calls)
}

func TestClaudegenContextualizer_SessionError_Propagates(t *testing.T) {
	c, err := extractor.NewClaudegenContextualizer(
		&stubRunner{err: errors.New("exec failure")},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)
	_, err = c.Provide(context.Background(), extractor.ContextInput{
		Entities: []extractor.Entity{{Name: "X"}},
	})
	require.Error(t, err)
}

func TestClaudegenContextualizer_MalformedOutput_ReturnsError(t *testing.T) {
	c, err := extractor.NewClaudegenContextualizer(
		&stubRunner{stdout: "not json"},
		&stubLoader{tpl: "t"},
	)
	require.NoError(t, err)
	_, err = c.Provide(context.Background(), extractor.ContextInput{
		Entities: []extractor.Entity{{Name: "X"}},
	})
	require.Error(t, err)
}

func TestNoopContextualizer_ReturnsNil(t *testing.T) {
	got, err := extractor.NewNoopContextualizer().Provide(context.Background(), extractor.ContextInput{
		Entities: []extractor.Entity{{Name: "X"}},
	})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestNewClaudegenContextualizer_NilArgs(t *testing.T) {
	_, err := extractor.NewClaudegenContextualizer(nil, &stubLoader{tpl: "t"})
	assert.Error(t, err)
	_, err = extractor.NewClaudegenContextualizer(&stubRunner{stdout: "{}"}, nil)
	assert.Error(t, err)
}

func TestPageContext_IsEmpty(t *testing.T) {
	// nil receiver
	var nilPC *extractor.PageContext
	assert.True(t, nilPC.IsEmpty())

	// 모두 empty
	pc := &extractor.PageContext{Background: nil, Timeline: nil, Implications: nil}
	assert.True(t, pc.IsEmpty())

	// background 있음 → not empty
	pc2 := &extractor.PageContext{Background: []extractor.BackgroundItem{{Subject: "X"}}}
	assert.False(t, pc2.IsEmpty())

	// implications 있음 → not empty
	pc3 := &extractor.PageContext{Implications: &extractor.Implications{Social: "x"}}
	assert.False(t, pc3.IsEmpty())
}
