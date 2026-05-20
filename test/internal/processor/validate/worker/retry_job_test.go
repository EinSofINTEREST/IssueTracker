// BuildRetryJob 의 ContentRef → CrawlJob 변환 + PriorityFromHeader 검증 (이슈 #523).
package worker_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/validate/worker"
	"issuetracker/pkg/queue"
)

func makeRetryMsg(t *testing.T, id, url, sourceName string, headers map[string]string) *queue.Message {
	t.Helper()
	ref := core.ContentRef{
		ID:  id,
		URL: url,
		SourceInfo: core.SourceInfo{
			Name: sourceName,
		},
	}
	refBytes, err := json.Marshal(ref)
	require.NoError(t, err)
	pm := core.ProcessingMessage{ID: id, Data: refBytes}
	b, err := json.Marshal(pm)
	require.NoError(t, err)
	return &queue.Message{Value: b, Headers: headers}
}

func TestBuildRetryJob_ValidRef_ReturnsCrawlJob(t *testing.T) {
	msg := makeRetryMsg(t, "ref-1", "https://example.com/x", "cnn", map[string]string{"priority": "1"})

	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "ref-1", job.ID)
	assert.Equal(t, "https://example.com/x", job.Target.URL)
	assert.Equal(t, "cnn", job.CrawlerName)
	assert.Equal(t, core.PriorityHigh, job.Priority)
	assert.Equal(t, core.TargetTypeArticle, job.Target.Type)
	assert.Equal(t, "validate_process_failed", job.Target.Metadata["retry_reason"])
	assert.Equal(t, "ref-1", job.Target.Metadata["original_ref_id"])
}

func TestBuildRetryJob_NoPriorityHeader_DefaultsToNormal(t *testing.T) {
	msg := makeRetryMsg(t, "ref-2", "https://example.com/", "src", nil)
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, core.PriorityNormal, job.Priority)
}

func TestBuildRetryJob_InvalidPriorityHeader_DefaultsToNormal(t *testing.T) {
	cases := []string{"0", "4", "abc", "-1"}
	for _, p := range cases {
		t.Run("priority="+p, func(t *testing.T) {
			msg := makeRetryMsg(t, "ref-x", "https://example.com/", "src", map[string]string{"priority": p})
			job, err := worker.BuildRetryJob(msg)
			require.NoError(t, err)
			assert.Equal(t, core.PriorityNormal, job.Priority)
		})
	}
}

func TestBuildRetryJob_HeaderCrawlerPreferredOverSourceInfo(t *testing.T) {
	msg := makeRetryMsg(t, "ref-h1", "https://example.com/", "source-info-name", map[string]string{"crawler": "header-crawler"})
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, "header-crawler", job.CrawlerName)
}

func TestBuildRetryJob_NoHeaderCrawler_UsesSourceInfo(t *testing.T) {
	msg := makeRetryMsg(t, "ref-h2", "https://example.com/", "fallback-source", map[string]string{})
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, "fallback-source", job.CrawlerName)
}

func TestBuildRetryJob_EmptyAllCrawler_UsesFallback(t *testing.T) {
	msg := makeRetryMsg(t, "ref-h3", "https://example.com/", "", nil)
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, "validate-retry", job.CrawlerName)
}

func TestBuildRetryJob_TargetTypeHeader_Category(t *testing.T) {
	msg := makeRetryMsg(t, "ref-tt-1", "https://example.com/", "src", map[string]string{"target_type": "category"})
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, core.TargetTypeCategory, job.Target.Type)
}

func TestBuildRetryJob_TargetTypeHeader_InvalidFallsBackToArticle(t *testing.T) {
	cases := []string{"unknown", "", "page", "list"}
	for _, tt := range cases {
		t.Run("type="+tt, func(t *testing.T) {
			msg := makeRetryMsg(t, "ref-tt-x", "https://example.com/", "src", map[string]string{"target_type": tt})
			job, err := worker.BuildRetryJob(msg)
			require.NoError(t, err)
			assert.Equal(t, core.TargetTypeArticle, job.Target.Type, "unknown %q → Article fallback", tt)
		})
	}
}

func TestBuildRetryJob_EmptyURL_ReturnsError(t *testing.T) {
	msg := makeRetryMsg(t, "ref-4", "", "src", nil)
	_, err := worker.BuildRetryJob(msg)
	assert.Error(t, err)
}

func TestBuildRetryJob_TimeoutHeaderHonored(t *testing.T) {
	// gemini #3275211693 — timeout_ms 헤더가 있으면 그 값 사용 (republishForReparse 와 동일 정책).
	msg := makeRetryMsg(t, "ref-to-1", "https://example.com/", "src", map[string]string{
		"timeout_ms": "60000", // 60s
	})
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, 60*time.Second, job.Timeout)
}

func TestBuildRetryJob_NoTimeoutHeader_UsesDefault(t *testing.T) {
	msg := makeRetryMsg(t, "ref-to-2", "https://example.com/", "src", nil)
	job, err := worker.BuildRetryJob(msg)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, job.Timeout, "헤더 부재 시 default 30s")
}

func TestBuildRetryJob_InvalidTimeoutHeader_UsesDefault(t *testing.T) {
	cases := []string{"0", "-1", "not-a-number", ""}
	for _, v := range cases {
		t.Run("timeout_ms="+v, func(t *testing.T) {
			msg := makeRetryMsg(t, "ref-to-x", "https://example.com/", "src", map[string]string{
				"timeout_ms": v,
			})
			job, err := worker.BuildRetryJob(msg)
			require.NoError(t, err)
			assert.Equal(t, 30*time.Second, job.Timeout, "잘못된 timeout_ms %q → default", v)
		})
	}
}

func TestBuildRetryJob_MalformedProcessingMessage_ReturnsError(t *testing.T) {
	msg := &queue.Message{Value: []byte("{not-json")}
	_, err := worker.BuildRetryJob(msg)
	assert.Error(t, err)
}

func TestBuildRetryJob_MalformedContentRef_ReturnsError(t *testing.T) {
	// ProcessingMessage.Data 는 json.RawMessage 라 marshal 시 valid JSON 여야 함.
	// 유효 JSON 이지만 ContentRef 스키마 외 형식 (배열) 으로 unmarshal 실패 유도.
	pm := core.ProcessingMessage{ID: "x", Data: json.RawMessage(`[1,2,3]`)}
	b, err := json.Marshal(pm)
	require.NoError(t, err)
	msg := &queue.Message{Value: b}
	_, err = worker.BuildRetryJob(msg)
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
		{"missing → normal", nil, 2},
		{"empty → normal", map[string]string{"priority": ""}, 2},
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
