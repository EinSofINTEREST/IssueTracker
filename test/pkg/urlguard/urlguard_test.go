package urlguard_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/logger"
	"issuetracker/pkg/urlguard"
)

// ─────────────────────────────────────────────────────────────────────────────
// PatternGuard 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestPatternGuard_Allow_TableDriven(t *testing.T) {
	g := urlguard.NewPatternGuard("/rss", "mailto:", "tel:")

	tests := []struct {
		name        string
		url         string
		wantAllowed bool
		wantReason  string
	}{
		{"empty url is allowed", "", true, ""},
		{"plain http url", "https://example.com/article/123", true, ""},
		{"rss path matches", "https://rss.cnn.com/rss/cnn_health.rss", false, "/rss"},
		{"mailto blocked", "mailto:foo@example.com", false, "mailto:"},
		{"tel blocked", "tel:+82-10-1234-5678", false, "tel:"},
		{"case-insensitive match", "https://RSS.CNN.COM/RSS/topstories.rss", false, "/rss"},
		{"non-rss domain unaffected", "https://news.naver.com/article/12345", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := g.Allow(tt.url)
			assert.Equal(t, tt.wantAllowed, got, "url=%q", tt.url)
			if !tt.wantAllowed {
				assert.True(t, strings.Contains(reason, tt.wantReason),
					"reason should mention pattern %q, got %q", tt.wantReason, reason)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestPatternGuard_NoPatterns_AllowsAll(t *testing.T) {
	g := urlguard.NewPatternGuard()
	allowed, reason := g.Allow("https://example.com/anything")
	assert.True(t, allowed)
	assert.Empty(t, reason)
}

func TestPatternGuard_EmptyPatternIgnored(t *testing.T) {
	g := urlguard.NewPatternGuard("", "/rss")
	allowed, _ := g.Allow("https://example.com/article")
	assert.True(t, allowed, "빈 패턴이 모든 URL 매칭하면 안 됨")
}

func TestPatternGuard_Concurrent_SafeAllow(t *testing.T) {
	g := urlguard.NewPatternGuard("/rss")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if i%2 == 0 {
					g.Allow("https://example.com/article")
				} else {
					g.Allow("https://rss.cnn.com/rss/foo.rss")
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestPatternGuard_PatternsCopied_FromCallerSlice(t *testing.T) {
	patterns := []string{"/rss"}
	g := urlguard.NewPatternGuard(patterns...)
	patterns[0] = "/something_else"

	allowed, _ := g.Allow("https://example.com/rss/foo")
	assert.False(t, allowed, "원본 슬라이스 변경이 Guard 패턴에 반영되면 안 됨")
}

func TestDefault_BlocksRSSAndMailto(t *testing.T) {
	g := urlguard.Default()

	tests := []struct {
		url     string
		blocked bool
	}{
		{"https://rss.cnn.com/rss/cnn_health.rss", true},
		{"mailto:user@example.com", true},
		{"tel:+82-10-0000", true},
		{"javascript:void(0)", true},
		{"https://news.naver.com/article", false},
		{"https://edition.cnn.com/health", false},
	}

	for _, tt := range tests {
		got, _ := g.Allow(tt.url)
		assert.Equal(t, !tt.blocked, got, "url=%q", tt.url)
	}
}

func TestAllowAllGuard(t *testing.T) {
	var g urlguard.Guard = urlguard.AllowAllGuard{}
	got, reason := g.Allow("https://rss.cnn.com/rss/foo.rss")
	assert.True(t, got)
	assert.Empty(t, reason)
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 테스트 — 단일 공통 컴포넌트
// ─────────────────────────────────────────────────────────────────────────────

// captureLogger 는 로그 출력을 bytes.Buffer 로 캡쳐하는 logger 를 반환합니다.
func captureLogger(buf *bytes.Buffer) *logger.Logger {
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelDebug
	return logger.New(cfg)
}

// extractLogEntries 는 JSON 줄 단위 로그를 map 슬라이스로 파싱합니다.
func extractLogEntries(t *testing.T, buf *bytes.Buffer) []map[string]interface{} {
	t.Helper()
	var entries []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var e map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		entries = append(entries, e)
	}
	return entries
}

func TestGate_Allow_Pass_NoLog(t *testing.T) {
	buf := &bytes.Buffer{}
	gate := urlguard.NewGate(urlguard.Default(), captureLogger(buf))

	ok := gate.Allow("https://edition.cnn.com/health", map[string]interface{}{"crawler": "cnn"})
	assert.True(t, ok)
	assert.Empty(t, buf.String(), "통과 시 로그 없음")
}

func TestGate_Allow_Block_LogsWarn(t *testing.T) {
	buf := &bytes.Buffer{}
	gate := urlguard.NewGate(urlguard.Default(), captureLogger(buf))

	ok := gate.Allow("https://rss.cnn.com/rss/cnn_health.rss",
		map[string]interface{}{"crawler": "cnn", "stage": "scheduler"})
	assert.False(t, ok)

	entries := extractLogEntries(t, buf)
	require.Len(t, entries, 1)
	assert.Equal(t, "warn", entries[0]["level"])
	assert.Equal(t, "blocked by url guard", entries[0]["message"])
	assert.Equal(t, "cnn", entries[0]["crawler"])
	assert.Equal(t, "scheduler", entries[0]["stage"])
	assert.Equal(t, "https://rss.cnn.com/rss/cnn_health.rss", entries[0]["url"])
	assert.Contains(t, entries[0]["reason"], "/rss")
}

func TestGate_Allow_NilFields_StillLogs(t *testing.T) {
	buf := &bytes.Buffer{}
	gate := urlguard.NewGate(urlguard.Default(), captureLogger(buf))

	ok := gate.Allow("https://rss.cnn.com/rss/foo.rss", nil)
	assert.False(t, ok)
	entries := extractLogEntries(t, buf)
	require.Len(t, entries, 1)
	assert.Equal(t, "warn", entries[0]["level"])
	assert.Contains(t, entries[0], "url")
	assert.Contains(t, entries[0], "reason")
}

func TestGate_Filter_PartialBlock(t *testing.T) {
	buf := &bytes.Buffer{}
	gate := urlguard.NewGate(urlguard.Default(), captureLogger(buf))

	urls := []string{
		"https://edition.cnn.com/article/1",
		"https://rss.cnn.com/rss/cnn_health.rss",
		"https://news.naver.com/main/read.naver?a=1",
		"mailto:foo@example.com",
	}
	allowed := gate.Filter(urls, map[string]interface{}{"crawler": "cnn", "stage": "publisher"})

	assert.Equal(t, []string{
		"https://edition.cnn.com/article/1",
		"https://news.naver.com/main/read.naver?a=1",
	}, allowed)

	entries := extractLogEntries(t, buf)
	assert.Len(t, entries, 2, "차단된 2건만 로그")
}

func TestGate_Filter_AllPass_NoLog(t *testing.T) {
	buf := &bytes.Buffer{}
	gate := urlguard.NewGate(urlguard.Default(), captureLogger(buf))

	urls := []string{"https://edition.cnn.com/a", "https://news.naver.com/b"}
	allowed := gate.Filter(urls, nil)
	assert.Equal(t, urls, allowed)
	assert.Empty(t, buf.String())
}

func TestGate_Filter_AllBlocked_ReturnsEmptyNotNil(t *testing.T) {
	gate := urlguard.NewGate(urlguard.Default(), captureLogger(&bytes.Buffer{}))

	allowed := gate.Filter([]string{
		"https://rss.cnn.com/rss/a.rss",
		"mailto:x@y.z",
	}, nil)
	require.NotNil(t, allowed, "모두 차단되어도 nil 이 아닌 빈 슬라이스 반환")
	assert.Empty(t, allowed)
}

func TestGate_Filter_EmptyInput(t *testing.T) {
	gate := urlguard.NewGate(urlguard.Default(), captureLogger(&bytes.Buffer{}))

	assert.Empty(t, gate.Filter(nil, nil))
	assert.Empty(t, gate.Filter([]string{}, nil))
}

func TestGate_Filter_DoesNotMutateInput(t *testing.T) {
	gate := urlguard.NewGate(urlguard.Default(), captureLogger(&bytes.Buffer{}))

	input := []string{"https://edition.cnn.com/a", "https://rss.cnn.com/rss/b.rss"}
	original := append([]string{}, input...)

	_ = gate.Filter(input, nil)
	assert.Equal(t, original, input, "Filter 가 입력 슬라이스를 변경하면 안 됨")
}

func TestNewGate_NilGuard_Panics(t *testing.T) {
	assert.Panics(t, func() {
		urlguard.NewGate(nil, nil)
	})
}

func TestNewGate_NilLogger_UsesDefault(t *testing.T) {
	// nil logger 는 panic 하지 않고 fallback 기본 logger 사용
	gate := urlguard.NewGate(urlguard.Default(), nil)
	require.NotNil(t, gate)
	// 호출이 panic 하지 않는지만 검증 (출력은 stdout 으로 흘러감)
	assert.NotPanics(t, func() {
		gate.Allow("https://rss.cnn.com/rss/foo.rss", nil)
	})
}
