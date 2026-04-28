package core_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "issuetracker/internal/crawler/core"
)

func newTestSource() core.SourceInfo {
	return core.SourceInfo{
		Country:  "US",
		Type:     core.SourceTypeNews,
		Name:     "cnn",
		BaseURL:  "https://www.cnn.com",
		Language: "en",
	}
}

func newTestTarget() core.Target {
	return core.Target{
		URL:  "https://edition.cnn.com/article/123",
		Type: core.TargetTypeArticle,
		Metadata: map[string]interface{}{
			"feed_url": "https://rss.cnn.com/foo.rss",
		},
	}
}

// TestNewRawContent_AllFields_Set:
// 8개 필드 모두 정확히 대입되는지 검증.
func TestNewRawContent_AllFields_Set(t *testing.T) {
	src := newTestSource()
	tgt := newTestTarget()
	headers := map[string]string{"Content-Type": "text/html", "Server": "nginx"}

	before := time.Now()
	raw := core.NewRawContent("cnn", src, tgt, "<html>body</html>", 200, headers)
	after := time.Now()

	require.NotNil(t, raw)
	assert.True(t, strings.HasPrefix(raw.ID, "cnn-"), "ID 는 'cnn-' prefix 로 시작해야 함")
	assert.Equal(t, src, raw.SourceInfo)
	assert.True(t, !raw.FetchedAt.Before(before) && !raw.FetchedAt.After(after),
		"FetchedAt 은 호출 시점 사이여야 함")
	assert.Equal(t, tgt.URL, raw.URL)
	assert.Equal(t, "<html>body</html>", raw.HTML)
	assert.Equal(t, 200, raw.StatusCode)
	assert.Equal(t, headers, raw.Headers)
	assert.Equal(t, tgt.Metadata, raw.Metadata)
}

// TestNewRawContent_NilHeaders_BecomesEmptyMap:
// nil headers 인자는 빈 map 으로 보정되어야 함 (downstream 에서 nil dereference 방지).
func TestNewRawContent_NilHeaders_BecomesEmptyMap(t *testing.T) {
	raw := core.NewRawContent("cnn", newTestSource(), newTestTarget(), "html", 200, nil)
	require.NotNil(t, raw.Headers)
	assert.Empty(t, raw.Headers)
	// 실제 사용 가능 — write 가 panic 하지 않음
	assert.NotPanics(t, func() {
		raw.Headers["X-Test"] = "value"
	})
}

// TestNewRawContent_IDFormat_UsesNamePrefix:
// ID format "<name>-<unix_nano>" 검증 — 다른 fetcher 가 다른 prefix 받는지 구분.
func TestNewRawContent_IDFormat_UsesNamePrefix(t *testing.T) {
	cnnRaw := core.NewRawContent("cnn", newTestSource(), newTestTarget(), "x", 200, nil)
	naverRaw := core.NewRawContent("naver", newTestSource(), newTestTarget(), "x", 200, nil)

	assert.True(t, strings.HasPrefix(cnnRaw.ID, "cnn-"))
	assert.True(t, strings.HasPrefix(naverRaw.ID, "naver-"))
	assert.NotEqual(t, cnnRaw.ID, naverRaw.ID, "다른 prefix → 다른 ID")
}

// TestNewRawContent_ConsecutiveCalls_DistinctIDs:
// 연속 호출 시 ID 가 모두 unique 해야 함.
// 신규 ID 형식 '<name>-<unix_nano>-<rand_hex>' 의 random suffix 덕분에 sleep 없이도 통과.
func TestNewRawContent_ConsecutiveCalls_DistinctIDs(t *testing.T) {
	ids := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		raw := core.NewRawContent("cnn", newTestSource(), newTestTarget(), "x", 200, nil)
		assert.False(t, ids[raw.ID], "중복 ID 발생 at i=%d: %s", i, raw.ID)
		ids[raw.ID] = true
	}
	assert.Len(t, ids, 1000, "1000회 연속 호출 모두 unique ID")
}

// TestNewRawContent_HighConcurrency_DistinctIDs:
// 동시 호출 (50 goroutine × 100회 = 5000건) 모두 unique 해야 함.
// random suffix 가 동일 nanosecond 충돌을 차단하는지 검증.
func TestNewRawContent_HighConcurrency_DistinctIDs(t *testing.T) {
	const goroutines = 50
	const callsPerGoroutine = 100

	idsCh := make(chan string, goroutines*callsPerGoroutine)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				raw := core.NewRawContent("cnn", newTestSource(), newTestTarget(), "x", 200, nil)
				idsCh <- raw.ID
			}
		}()
	}
	wg.Wait()
	close(idsCh)

	seen := make(map[string]bool, goroutines*callsPerGoroutine)
	collisions := 0
	for id := range idsCh {
		if seen[id] {
			collisions++
		}
		seen[id] = true
	}
	assert.Equal(t, 0, collisions, "동시 5000건 호출에서 ID 충돌 발생")
	assert.Len(t, seen, goroutines*callsPerGoroutine, "모든 ID 가 unique")
}

// TestNewRawContent_NilMetadata_PreservedAsNil:
// target.Metadata 가 nil 이면 그대로 nil 보존 (호출자가 nil 여부로 분기 가능).
func TestNewRawContent_NilMetadata_PreservedAsNil(t *testing.T) {
	tgt := core.Target{URL: "https://example.com", Type: core.TargetTypeArticle, Metadata: nil}
	raw := core.NewRawContent("cnn", newTestSource(), tgt, "x", 200, nil)
	assert.Nil(t, raw.Metadata, "Metadata 는 nil 그대로 보존")
}

// TestNewRawContent_MetadataReference_NotCopied:
// target.Metadata 는 원본 reference 를 그대로 사용 (deep copy 없음).
// 호출자가 partial_load 같은 변형이 필요하면 호출 후 raw.Metadata 를 덮어써야 함.
func TestNewRawContent_MetadataReference_NotCopied(t *testing.T) {
	tgt := newTestTarget()
	raw := core.NewRawContent("cnn", newTestSource(), tgt, "x", 200, nil)

	// 같은 reference — target.Metadata 변경 시 raw.Metadata 도 영향 받음
	tgt.Metadata["new_key"] = "new_value"
	assert.Equal(t, "new_value", raw.Metadata["new_key"],
		"NewRawContent 는 target.Metadata reference 를 그대로 사용 (deep copy 없음)")
}
