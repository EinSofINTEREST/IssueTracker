package goquery_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	gqimpl "issuetracker/internal/processor/fetcher/implementation/goquery"
)

// euckrBytes 는 "네이버뉴스" 를 EUC-KR 로 인코딩한 바이트입니다.
// Python: "네이버뉴스".encode("euc-kr") → b'\xb3×\xc0̹ö\xb4º\xbd º'
var euckrTitle = []byte{0xb3, 0xd7, 0xc0, 0xcc, 0xb9, 0xf6, 0xb4, 0xba, 0xbd, 0xba} // 네이버뉴스 in EUC-KR

func newTestCrawler(name string) *gqimpl.GoqueryCrawler {
	cfg := core.DefaultConfig()
	cfg.UserAgent = "test-agent"
	info := core.SourceInfo{Name: name, Country: "KR", Language: "ko"}
	return gqimpl.NewGoqueryCrawler(name, info, cfg)
}

// TestFetch_EUCKR_ConvertedToUTF8 는 EUC-KR Content-Type 헤더를 가진 응답이
// UTF-8 로 변환되어 RawContent.HTML 에 저장되는지 검증합니다 (이슈 #253).
func TestFetch_EUCKR_ConvertedToUTF8(t *testing.T) {
	// EUC-KR 인코딩된 HTML — Content-Type 에 charset 명시
	body := append(
		[]byte(`<html><head><meta http-equiv="Content-Type" content="text/html; charset=euc-kr"></head><body><h1>`),
		euckrTitle...,
	)
	body = append(body, []byte(`</h1></body></html>`)...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=euc-kr")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	crawler := newTestCrawler("test-euckr")
	raw, err := crawler.Fetch(t.Context(), core.Target{URL: srv.URL, Type: core.TargetTypeArticle})

	require.NoError(t, err)
	require.NotNil(t, raw)
	// UTF-8 변환 후 HTML은 유효한 UTF-8 문자열이어야 합니다.
	assert.True(t, utf8.ValidString(raw.HTML), "RawContent.HTML must be valid UTF-8 after charset conversion")
	// 제목 텍스트 "네이버뉴스"가 올바르게 변환되어야 합니다.
	assert.True(t, strings.Contains(raw.HTML, "네이버뉴스"),
		"EUC-KR title should be converted to UTF-8 '네이버뉴스'")
}

// TestFetch_UTF8_NoConversion 은 이미 UTF-8 인 응답이 손상 없이 저장되는지 검증합니다.
func TestFetch_UTF8_NoConversion(t *testing.T) {
	body := `<html><head></head><body><h1>네이버뉴스</h1></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	crawler := newTestCrawler("test-utf8")
	raw, err := crawler.Fetch(t.Context(), core.Target{URL: srv.URL, Type: core.TargetTypeArticle})

	require.NoError(t, err)
	require.NotNil(t, raw)
	assert.True(t, utf8.ValidString(raw.HTML), "UTF-8 response must remain valid UTF-8")
	assert.True(t, strings.Contains(raw.HTML, "네이버뉴스"))
}

// TestFetch_NoCharsetHeader_FallbackToUTF8 은 Content-Type 에 charset 이 없는 경우
// graceful degrade (원본 body 사용) 해도 빌드/실행이 깨지지 않음을 검증합니다.
func TestFetch_NoCharsetHeader_FallbackToUTF8(t *testing.T) {
	body := `<html><head></head><body><p>hello world</p></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	crawler := newTestCrawler("test-no-charset")
	raw, err := crawler.Fetch(t.Context(), core.Target{URL: srv.URL, Type: core.TargetTypeArticle})

	require.NoError(t, err)
	require.NotNil(t, raw)
	assert.True(t, strings.Contains(raw.HTML, "hello world"))
}
