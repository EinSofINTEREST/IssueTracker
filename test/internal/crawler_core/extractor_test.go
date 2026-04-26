package core_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "issuetracker/internal/crawler/core"
)

func TestHTMLLinkExtractor_NilRaw_ReturnsInternalCrawlerError(t *testing.T) {
	e := core.NewHTMLLinkExtractor("https://example.com")

	links, err := e.Extract(nil)

	assert.Nil(t, links)
	require.Error(t, err)

	var ce *core.CrawlerError
	require.True(t, errors.As(err, &ce), "에러는 *core.CrawlerError 로 확정되어야 함")
	assert.Equal(t, core.ErrCategoryInternal, ce.Category)
	assert.Equal(t, core.CodeInternal, ce.Code)
}

func TestHTMLLinkExtractor_InnerError_WrapsAsParseCrawlerError(t *testing.T) {
	// pkg/links 의 inner.Extract 는 raw.URL 이 url.Parse 실패하는 값일 때
	// fmt.Errorf("resolve base url: ...") 를 반환합니다.
	// 이 에러가 boundary 에서 NewParseError(CodeParseHTML, ...) 로 변환되는지 검증.
	e := core.NewHTMLLinkExtractor("https://example.com")

	raw := &core.RawContent{
		URL:  "%/", // url.Parse 가 "invalid URL escape" 로 실패
		HTML: `<html><body><a href="/foo">link</a></body></html>`,
	}

	links, err := e.Extract(raw)

	assert.Nil(t, links)
	require.Error(t, err)

	var ce *core.CrawlerError
	require.True(t, errors.As(err, &ce), "boundary 에러는 *core.CrawlerError 여야 함")
	assert.Equal(t, core.ErrCategoryParse, ce.Category)
	assert.Equal(t, core.CodeParseHTML, ce.Code)
	assert.Equal(t, raw.URL, ce.URL, "URL 필드는 raw.URL 을 보존해야 함")
	assert.NotNil(t, ce.Unwrap(), "내부 cause 가 wrap 되어야 함")
}

func TestHTMLLinkExtractor_ValidInput_NoError(t *testing.T) {
	e := core.NewHTMLLinkExtractor("https://example.com")

	raw := &core.RawContent{
		URL: "https://example.com/page",
		HTML: `<html><body>
			<a href="/foo">internal</a>
			<a href="https://other.com/bar">external</a>
		</body></html>`,
	}

	links, err := e.Extract(raw)
	require.NoError(t, err)
	assert.NotEmpty(t, links)
}
