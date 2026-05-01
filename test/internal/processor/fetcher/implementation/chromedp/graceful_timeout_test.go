package chromedp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	cdpimpl "issuetracker/internal/processor/fetcher/implementation/chromedp"
)

// TestIsTimeoutError_DeadlineExceeded_True:
// chromedp.Run 이 timeout 으로 종료되면 context.DeadlineExceeded 를 wrap 하여 반환.
// 본 helper 가 그 케이스를 정확히 식별해야 graceful capture 분기로 진입 가능.
func TestIsTimeoutError_DeadlineExceeded_True(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	assert.True(t, cdpimpl.IsTimeoutError(ctx.Err()))
	assert.True(t, cdpimpl.IsTimeoutError(context.DeadlineExceeded))
}

// TestIsTimeoutError_Canceled_False:
// 사용자/상위 호출자가 context 를 cancel 한 케이스는 graceful capture 대상이 아니다.
// IsTimeoutError 는 false 를 반환하여 호출자가 즉시 에러로 분류하도록 한다.
func TestIsTimeoutError_Canceled_False(t *testing.T) {
	assert.False(t, cdpimpl.IsTimeoutError(context.Canceled))
}

// TestIsTimeoutError_Nil_False:
// nil 에러는 timeout 이 아니다.
func TestIsTimeoutError_Nil_False(t *testing.T) {
	assert.False(t, cdpimpl.IsTimeoutError(nil))
}

// TestIsTimeoutError_OtherError_False:
// 일반 에러는 timeout 이 아니다 — graceful capture 분기 미진입.
func TestIsTimeoutError_OtherError_False(t *testing.T) {
	assert.False(t, cdpimpl.IsTimeoutError(errors.New("network refused")))
}

// TestIsValidPartialDOM_Cases:
// 부분 DOM 검증의 핵심 두 조건 — 길이 (>= 512) + body 태그 존재 — 을 케이스로 검증.
// 본 검증은 이슈 #146 이후 "검증 실패해도 partial 로 진행" 정책이지만, helper 자체는
// 다운스트림이 partial 신뢰도를 판단할 때 활용하므로 동작은 유지.
func TestIsValidPartialDOM_Cases(t *testing.T) {
	body := strings.Repeat("a", 600)

	tests := []struct {
		name string
		html string
		want bool
	}{
		{
			name: "valid lowercase body tag",
			html: "<html><head></head><body>" + body + "</body></html>",
			want: true,
		},
		{
			name: "valid uppercase body tag",
			html: "<HTML><HEAD></HEAD><BODY>" + body + "</BODY></HTML>",
			want: true,
		},
		{
			name: "below minimum length",
			html: "<html><body>tiny</body></html>",
			want: false,
		},
		{
			name: "long enough but no body tag",
			html: strings.Repeat("<div>filler</div>", 100),
			want: false,
		},
		{
			name: "empty html",
			html: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cdpimpl.IsValidPartialDOM(tt.html))
		})
	}
}

// TestDefaultGracefulCaptureTimeout_Value:
// 이슈 #146 의 기본값 합의: 부하 상태 CDP 응답 + page.StopLoading 후 OuterHTML 회수까지
// 충분한 여유를 위해 10s 채택. 의도치 않은 변경 방지 차원의 sentinel 테스트.
func TestDefaultGracefulCaptureTimeout_Value(t *testing.T) {
	assert.Equal(t, 10*time.Second, cdpimpl.DefaultGracefulCaptureTimeout)
}

// TestDefaultOptions_GracefulCaptureTimeoutPopulated:
// DefaultOptions / DefaultRemoteOptions 모두 GracefulCaptureTimeout 가 채워져
// captureOuterHTML 호출 시 0 fallback 분기를 타지 않도록 보장.
func TestDefaultOptions_GracefulCaptureTimeoutPopulated(t *testing.T) {
	local := cdpimpl.DefaultOptions()
	assert.Equal(t, cdpimpl.DefaultGracefulCaptureTimeout, local.GracefulCaptureTimeout)

	remote := cdpimpl.DefaultRemoteOptions()
	assert.Equal(t, cdpimpl.DefaultGracefulCaptureTimeout, remote.GracefulCaptureTimeout)
}

// TestMetadataKeyPartialLoad_Stable:
// 다운스트림 (parser, validator) 이 metadata 에서 partial 식별 시 의존하는 키.
// 외부 계약이라 변경 시 다운스트림에도 영향 — sentinel 로 보호.
func TestMetadataKeyPartialLoad_Stable(t *testing.T) {
	assert.Equal(t, "partial_load", cdpimpl.MetadataKeyPartialLoad)
}
