package chromedp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	chromedpimpl "issuetracker/internal/crawler/implementation/chromedp"
)

// TestIsTimeoutError_NilError_ReturnsFalse: nil 에러는 timeout이 아님
func TestIsTimeoutError_NilError_ReturnsFalse(t *testing.T) {
	assert.False(t, chromedpimpl.IsTimeoutError(nil))
}

// TestIsTimeoutError_DeadlineExceeded_ReturnsTrue:
// chromedp.Run이 timeout으로 종료되면 context.DeadlineExceeded를 wrap하여 반환하므로
// errors.Is 매칭이 true를 반환해야 한다.
func TestIsTimeoutError_DeadlineExceeded_ReturnsTrue(t *testing.T) {
	assert.True(t, chromedpimpl.IsTimeoutError(context.DeadlineExceeded))
}

// TestIsTimeoutError_WrappedDeadlineExceeded_ReturnsTrue:
// fmt.Errorf("%w") 로 wrap된 에러도 errors.Is로 식별 가능해야 한다.
// chromedp 내부의 다양한 wrap 형태를 시뮬레이션한다.
func TestIsTimeoutError_WrappedDeadlineExceeded_ReturnsTrue(t *testing.T) {
	wrapped := fmt.Errorf("chromedp action failed: %w", context.DeadlineExceeded)
	assert.True(t, chromedpimpl.IsTimeoutError(wrapped))
}

// TestIsTimeoutError_StringConcatNotWrapped_ReturnsFalse:
// 문자열 결합으로 만든 에러(에러 chain 미보존)는 식별되지 않아야 한다.
// errors.Is 의 정상 동작을 보장하는 sanity check.
func TestIsTimeoutError_StringConcatNotWrapped_ReturnsFalse(t *testing.T) {
	plain := errors.New("chromedp action failed: " + context.DeadlineExceeded.Error())
	assert.False(t, chromedpimpl.IsTimeoutError(plain))
}

// TestIsTimeoutError_Canceled_ReturnsFalse:
// 사용자 취소(context.Canceled)는 graceful capture 대상이 아니므로 false 반환해야 한다.
func TestIsTimeoutError_Canceled_ReturnsFalse(t *testing.T) {
	assert.False(t, chromedpimpl.IsTimeoutError(context.Canceled))
}

// TestIsTimeoutError_OtherError_ReturnsFalse: 기타 일반 에러는 false
func TestIsTimeoutError_OtherError_ReturnsFalse(t *testing.T) {
	assert.False(t, chromedpimpl.IsTimeoutError(errors.New("some other error")))
}

// TestIsValidPartialDOM_ValidHTML_ReturnsTrue:
// 충분한 길이와 <body> 태그를 포함한 HTML은 유효한 부분 로드로 인정
func TestIsValidPartialDOM_ValidHTML_ReturnsTrue(t *testing.T) {
	html := "<html><head><title>Test</title></head><body>" +
		strings.Repeat("<p>some real content here for length</p>", 20) +
		"</body></html>"
	assert.True(t, chromedpimpl.IsValidPartialDOM(html))
}

// TestIsValidPartialDOM_TooShort_ReturnsFalse:
// 최소 길이 미만의 HTML은 의미 있는 컨텐츠가 없다고 판단하여 거부
func TestIsValidPartialDOM_TooShort_ReturnsFalse(t *testing.T) {
	html := "<html><body>tiny</body></html>"
	assert.False(t, chromedpimpl.IsValidPartialDOM(html))
}

// TestIsValidPartialDOM_NoBody_ReturnsFalse:
// <body> 태그가 없는 HTML은 DOM 구조가 불완전하다고 판단
func TestIsValidPartialDOM_NoBody_ReturnsFalse(t *testing.T) {
	// 길이는 충족하지만 body 태그 없음
	html := "<html><head>" + strings.Repeat("<meta name='x' content='y'>", 50) + "</head></html>"
	assert.False(t, chromedpimpl.IsValidPartialDOM(html))
}

// TestIsValidPartialDOM_BodyUpperCase_ReturnsTrue:
// case-insensitive 매칭으로 대문자 BODY 태그도 인정
func TestIsValidPartialDOM_BodyUpperCase_ReturnsTrue(t *testing.T) {
	html := "<HTML><HEAD></HEAD><BODY " +
		strings.Repeat("class='abcdef' ", 50) +
		">content</BODY></HTML>"
	assert.True(t, chromedpimpl.IsValidPartialDOM(html))
}

// TestIsValidPartialDOM_EmptyString_ReturnsFalse: 빈 문자열은 유효하지 않음
func TestIsValidPartialDOM_EmptyString_ReturnsFalse(t *testing.T) {
	assert.False(t, chromedpimpl.IsValidPartialDOM(""))
}

// TestIsValidPartialDOM_BodyWithAttributes_ReturnsTrue:
// `<body class="...">` 형태의 attribute가 붙은 body 태그도 매칭되어야 함
// (substring "<body" 검사이므로 자연스럽게 처리)
func TestIsValidPartialDOM_BodyWithAttributes_ReturnsTrue(t *testing.T) {
	html := "<html><head>" +
		strings.Repeat("<link rel='stylesheet'>", 30) +
		"</head><body class='dark-theme'>partial content</body></html>"
	assert.True(t, chromedpimpl.IsValidPartialDOM(html))
}

// TestMetadataKeyPartialLoad_ConstantValue:
// 다운스트림 소비자가 동일 키를 참조하므로 상수 값이 변경되지 않음을 보장한다.
// 변경 시 다운스트림 호환성 영향이 발생하므로 명시적 회귀 테스트로 잠금.
func TestMetadataKeyPartialLoad_ConstantValue(t *testing.T) {
	assert.Equal(t, "partial_load", chromedpimpl.MetadataKeyPartialLoad)
}
