package news_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/pkg/logger"
)

// stubFetcher 는 NewsFetcher 인터페이스를 만족하는 테스트용 더블입니다.
// 지정된 raw/err 를 그대로 반환합니다.
type stubFetcher struct {
	raw *core.RawContent
	err error
}

func (s *stubFetcher) Fetch(_ context.Context, _ core.Target) (*core.RawContent, error) {
	return s.raw, s.err
}

// captureLogger 는 io.Writer 로 bytes.Buffer 를 사용하는 logger 를 생성합니다.
// Debug 레벨로 설정하여 DEBUG/ERROR 모든 로그를 캡쳐합니다.
func captureLogger(buf *bytes.Buffer) *logger.Logger {
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelDebug
	return logger.New(cfg)
}

// extractLevels 는 JSON 줄 단위 로그에서 'level' 필드 값들을 추출합니다.
func extractLevels(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	var levels []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if lvl, ok := entry["level"].(string); ok {
			levels = append(levels, lvl)
		}
	}
	return levels
}

// TestBrowserFetchHandler_NormalTimeout_LogsError:
// 회귀 방지 — chromedp CDP_006 처럼 errors.Join 에 context.Canceled 가 포함된 정상 timeout
// 케이스가 호출자 ctx 와 무관하게 ERROR 로 기록되어야 함을 검증합니다.
//
// 이전 구현(errors.Is(err, context.Canceled) || ctx.Err() != nil) 은 chromedp 내부 cleanup
// 으로 발생한 context.Canceled 를 셧다운으로 오인하여 정상 timeout 을 DEBUG 로 강등시키는
// false positive 가 있었습니다. 본 테스트는 그 회귀를 차단합니다.
func TestBrowserFetchHandler_NormalTimeout_LogsError(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogger(buf)

	// chromedp 의 CDP_006 형태: errors.Join(deadline, canceled)
	// 외부에서 보면 errors.Is(err, context.Canceled) == true
	chromedpErr := &core.CrawlerError{
		Category: core.ErrCategoryTimeout,
		Code:     "CDP_006",
		Message:  "render timeout and graceful capture failed",
		Err:      errors.Join(context.DeadlineExceeded, context.Canceled),
	}
	fetcher := &stubFetcher{err: chromedpErr}

	handler := news.NewBrowserFetchHandler(fetcher, log)

	// 호출자 ctx 는 정상 (셧다운 발화 전)
	_, err := handler.Handle(context.Background(), categoryJob("https://example.com"))
	require.Error(t, err)

	levels := extractLevels(t, buf)
	require.NotEmpty(t, levels)
	assert.Contains(t, levels, "error",
		"호출자 ctx 가 정상이면 errors.Is(canceled) 가 true 여도 ERROR 로 기록되어야 함 — "+
			"chromedp 내부 cleanup cancel 을 셧다운으로 오인 금지")
	assert.NotContains(t, levels, "debug",
		"정상 timeout 케이스가 셧다운으로 오인되어 DEBUG 로 강등되면 안 됨")
}

// TestBrowserFetchHandler_ShutdownCanceled_LogsDebug:
// 호출자 ctx 가 cancel 된 셧다운 케이스에서는 DEBUG 로 강등되어야 함.
func TestBrowserFetchHandler_ShutdownCanceled_LogsDebug(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogger(buf)

	fetcher := &stubFetcher{err: errors.New("any fetch error")}
	handler := news.NewBrowserFetchHandler(fetcher, log)

	// 호출자 ctx 가 이미 cancel 된 상태 (셧다운 시뮬레이션)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := handler.Handle(ctx, categoryJob("https://example.com"))
	require.Error(t, err)

	levels := extractLevels(t, buf)
	require.NotEmpty(t, levels)
	assert.Contains(t, levels, "debug",
		"호출자 ctx 가 cancel 된 셧다운 케이스는 DEBUG 로 강등되어야 함")
	assert.NotContains(t, levels, "error",
		"셧다운 케이스가 ERROR 로 기록되어 알림 노이즈를 만들면 안 됨")
}

// TestBrowserFetchHandler_PlainNetworkError_LogsError:
// 일반 네트워크 에러(canceled chain 없음) + 정상 ctx → ERROR (정상 동작 확인).
func TestBrowserFetchHandler_PlainNetworkError_LogsError(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogger(buf)

	fetcher := &stubFetcher{err: errors.New("dial tcp: connection refused")}
	handler := news.NewBrowserFetchHandler(fetcher, log)

	_, err := handler.Handle(context.Background(), categoryJob("https://example.com"))
	require.Error(t, err)

	levels := extractLevels(t, buf)
	require.NotEmpty(t, levels)
	assert.Contains(t, levels, "error",
		"일반 네트워크 에러는 ERROR 로 기록되어야 함")
}
