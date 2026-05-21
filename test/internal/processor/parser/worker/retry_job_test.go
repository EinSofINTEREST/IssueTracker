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

func TestBuildRetryJob_HeaderCrawlerPreferredOverSourceInfo(t *testing.T) {
	// crawler 헤더가 있으면 SourceInfo.Name 보다 우선 (gemini #3274689308).
	msg := &queue.Message{
		Value: makeRawRef(t, "raw-h1", "https://example.com/", "source-info-name"),
		Headers: map[string]string{
			"crawler": "header-crawler",
		},
	}
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, "header-crawler", job.CrawlerName)
}

func TestBuildRetryJob_NoHeaderCrawler_UsesSourceInfo(t *testing.T) {
	// crawler 헤더가 없으면 RawContentRef.SourceInfo.Name fallback.
	msg := &queue.Message{
		Value:   makeRawRef(t, "raw-h2", "https://example.com/", "fallback-source"),
		Headers: map[string]string{},
	}
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, "fallback-source", job.CrawlerName)
}

func TestBuildRetryJob_TargetTypeHeader_Category(t *testing.T) {
	// target_type 헤더가 유효 (category) 면 사용 (gemini #3274689285 — Article 오인 차단).
	msg := &queue.Message{
		Value: makeRawRef(t, "raw-tt-1", "https://example.com/", "src"),
		Headers: map[string]string{
			"target_type": "category",
		},
	}
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, core.TargetTypeCategory, job.Target.Type)
}

func TestBuildRetryJob_TargetTypeHeader_Article(t *testing.T) {
	msg := &queue.Message{
		Value: makeRawRef(t, "raw-tt-2", "https://example.com/", "src"),
		Headers: map[string]string{
			"target_type": "article",
		},
	}
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, core.TargetTypeArticle, job.Target.Type)
}

func TestBuildRetryJob_TargetTypeHeader_InvalidFallsBackToArticle(t *testing.T) {
	cases := []string{"unknown", "", "page", "list"}
	for _, tt := range cases {
		t.Run("type="+tt, func(t *testing.T) {
			msg := &queue.Message{
				Value: makeRawRef(t, "raw-tt-x", "https://example.com/", "src"),
				Headers: map[string]string{
					"target_type": tt,
				},
			}
			job, err := worker.BuildRetryJob(msg)
			require.NoError(t, err)
			assert.Equal(t, core.TargetTypeArticle, job.Target.Type,
				"unknown target_type %q should fall back to Article", tt)
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
