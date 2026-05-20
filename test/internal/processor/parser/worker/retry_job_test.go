// BuildRetryJob 의 RawContentRef → CrawlJob 변환 검증 + PriorityFromHeader (이슈 #522).
package worker_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/worker"
	"issuetracker/pkg/queue"
)

func makeRawRef(t *testing.T, id, url, sourceName string) []byte {
	t.Helper()
	ref := core.RawContentRef{
		ID:  id,
		URL: url,
		SourceInfo: core.SourceInfo{
			Name: sourceName,
		},
	}
	b, err := json.Marshal(ref)
	require.NoError(t, err)
	return b
}

func TestBuildRetryJob_ValidRef_ReturnsCrawlJob(t *testing.T) {
	msg := &queue.Message{
		Value: makeRawRef(t, "raw-1", "https://example.com/x", "cnn"),
		Headers: map[string]string{
			"priority": "1",
		},
	}

	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "raw-1", job.ID)
	assert.Equal(t, "https://example.com/x", job.Target.URL)
	assert.Equal(t, "cnn", job.CrawlerName)
	assert.Equal(t, core.PriorityHigh, job.Priority)
	assert.Equal(t, core.TargetTypeArticle, job.Target.Type)
	assert.Equal(t, "parser_process_failed", job.Target.Metadata["retry_reason"])
	assert.Equal(t, "raw-1", job.Target.Metadata["original_raw_id"])
}

func TestBuildRetryJob_NoPriorityHeader_DefaultsToNormal(t *testing.T) {
	msg := &queue.Message{
		Value:   makeRawRef(t, "raw-2", "https://example.com/", "src"),
		Headers: map[string]string{},
	}

	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, core.PriorityNormal, job.Priority)
}

func TestBuildRetryJob_InvalidPriorityHeader_DefaultsToNormal(t *testing.T) {
	cases := []string{"0", "4", "abc", "-1"}
	for _, p := range cases {
		t.Run("priority="+p, func(t *testing.T) {
			msg := &queue.Message{
				Value: makeRawRef(t, "raw-x", "https://example.com/", "src"),
				Headers: map[string]string{
					"priority": p,
				},
			}
			job, err := worker.BuildRetryJob(msg)
			require.NoError(t, err)
			assert.Equal(t, core.PriorityNormal, job.Priority)
		})
	}
}

func TestBuildRetryJob_EmptySourceName_UsesFallback(t *testing.T) {
	msg := &queue.Message{
		Value: makeRawRef(t, "raw-3", "https://example.com/", ""),
	}
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, "parser-retry", job.CrawlerName)
}

func TestBuildRetryJob_EmptyURL_ReturnsError(t *testing.T) {
	msg := &queue.Message{
		Value: makeRawRef(t, "raw-4", "", "src"),
	}
	_, err := worker.BuildRetryJob(msg)
	assert.Error(t, err)
}

func TestBuildRetryJob_MalformedJSON_ReturnsError(t *testing.T) {
	msg := &queue.Message{
		Value: []byte("{not-valid-json"),
	}
	_, err := worker.BuildRetryJob(msg)
	assert.Error(t, err)
}

func TestPriorityFromHeader_Mapping(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"high (1)", map[string]string{"priority": "1"}, 1},
		{"normal (2)", map[string]string{"priority": "2"}, 2},
		{"low (3)", map[string]string{"priority": "3"}, 3},
		{"missing key → normal", nil, 2},
		{"empty value → normal", map[string]string{"priority": ""}, 2},
		{"non-numeric → normal", map[string]string{"priority": "xx"}, 2},
		{"out of range 0 → normal", map[string]string{"priority": "0"}, 2},
		{"out of range 4 → normal", map[string]string{"priority": "4"}, 2},
		{"negative → normal", map[string]string{"priority": "-1"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := worker.PriorityFromHeader(tt.headers)
			assert.Equal(t, tt.want, got)
		})
	}
}
