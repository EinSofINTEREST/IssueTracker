package precheck_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/precheck"
)

// fakeSource 는 미리 지정된 verdict 를 반환하는 stub 입니다.
type fakeSource struct {
	name    string
	verdict precheck.Verdict
	calls   int
}

func (s *fakeSource) Name() string { return s.name }
func (s *fakeSource) Check(_ context.Context, _ string) precheck.Decision {
	s.calls++
	return precheck.Decision{Verdict: s.verdict, Reason: s.name + ":test", Source: s.name}
}

func TestDecider_AllAllow_ReturnsAllow(t *testing.T) {
	a := &fakeSource{name: "a", verdict: precheck.VerdictAllow}
	b := &fakeSource{name: "b", verdict: precheck.VerdictAllow}
	d := precheck.New(a, b)

	dec := d.CheckURL(context.Background(), "https://example.com")
	assert.Equal(t, precheck.VerdictAllow, dec.Verdict)
	assert.Equal(t, 1, a.calls)
	assert.Equal(t, 1, b.calls)
}

func TestDecider_FirstNonAllow_ShortCircuits(t *testing.T) {
	a := &fakeSource{name: "a", verdict: precheck.VerdictAllow}
	b := &fakeSource{name: "b", verdict: precheck.VerdictDrop}
	c := &fakeSource{name: "c", verdict: precheck.VerdictAllow}
	d := precheck.New(a, b, c)

	dec := d.CheckURL(context.Background(), "https://example.com")
	assert.Equal(t, precheck.VerdictDrop, dec.Verdict)
	assert.Equal(t, "b", dec.Source)
	assert.Equal(t, 1, a.calls)
	assert.Equal(t, 1, b.calls)
	assert.Equal(t, 0, c.calls, "Drop 결정 후 후속 Source 는 호출되지 않음")
}

func TestDecider_ExtractLinksOnly_PropagatesVerdict(t *testing.T) {
	a := &fakeSource{name: "a", verdict: precheck.VerdictExtractLinksOnly}
	d := precheck.New(a)

	dec := d.CheckURL(context.Background(), "https://example.com")
	assert.Equal(t, precheck.VerdictExtractLinksOnly, dec.Verdict)
	assert.Equal(t, "a", dec.Source)
}

func TestDecider_NilSourceFiltered(t *testing.T) {
	a := &fakeSource{name: "a", verdict: precheck.VerdictAllow}
	// nil source 는 New 가 자동 필터링 — wiring 단계 conditional 활성화 (예: BLACKLIST_ENABLED=false).
	d := precheck.New(nil, a, nil)

	dec := d.CheckURL(context.Background(), "https://example.com")
	assert.Equal(t, precheck.VerdictAllow, dec.Verdict)
	assert.Equal(t, 1, a.calls)
}

func TestDecider_EmptySources_AllowsAll(t *testing.T) {
	d := precheck.New()
	dec := d.CheckURL(context.Background(), "https://example.com")
	assert.Equal(t, precheck.VerdictAllow, dec.Verdict)
}

func TestDecider_CheckURLs_BatchOrder(t *testing.T) {
	// 첫 번째 URL 만 차단하는 Source — index 별 다른 verdict 반환을 위한 inline 구현.
	type batchSource struct {
		blockHost string
	}
	// 위 inline 구조체는 메서드 정의를 못 하므로 별도 함수형 source.
	src := dynamicSource{blockedURL: "https://block.example.com"}
	d := precheck.New(src)

	urls := []string{
		"https://allow.example.com",
		"https://block.example.com",
		"https://other.example.com",
	}
	decisions := d.CheckURLs(context.Background(), urls)
	assert.Len(t, decisions, 3)
	assert.Equal(t, precheck.VerdictAllow, decisions[0].Verdict)
	assert.Equal(t, precheck.VerdictDrop, decisions[1].Verdict)
	assert.Equal(t, precheck.VerdictAllow, decisions[2].Verdict)
	_ = batchSource{} // 사용하지 않음 — type assertion 위해 정의했으나 dynamicSource 로 대체
}

func TestDecider_CheckURLs_EmptyInput(t *testing.T) {
	d := precheck.New()
	decisions := d.CheckURLs(context.Background(), nil)
	assert.Nil(t, decisions)
}

// dynamicSource 는 URL 별로 다른 verdict 를 반환하는 stub 입니다.
type dynamicSource struct {
	blockedURL string
}

func (s dynamicSource) Name() string { return "dynamic" }
func (s dynamicSource) Check(_ context.Context, url string) precheck.Decision {
	if url == s.blockedURL {
		return precheck.Decision{Verdict: precheck.VerdictDrop, Reason: "blocked", Source: "dynamic"}
	}
	return precheck.Decision{Verdict: precheck.VerdictAllow}
}

func TestVerdict_String(t *testing.T) {
	assert.Equal(t, "allow", precheck.VerdictAllow.String())
	assert.Equal(t, "drop", precheck.VerdictDrop.String())
	assert.Equal(t, "extract_links_only", precheck.VerdictExtractLinksOnly.String())
}

func TestDecision_Allowed(t *testing.T) {
	assert.True(t, precheck.Decision{Verdict: precheck.VerdictAllow}.Allowed())
	assert.False(t, precheck.Decision{Verdict: precheck.VerdictDrop}.Allowed())
	assert.False(t, precheck.Decision{Verdict: precheck.VerdictExtractLinksOnly}.Allowed())
}

func TestDecider_AutoFillsSourceName(t *testing.T) {
	// Source 가 Decision.Source 를 비워둬도 Decider 가 short-circuit 시 자동 채움.
	src := emptySourceSource{}
	d := precheck.New(src)

	dec := d.CheckURL(context.Background(), "https://example.com")
	assert.Equal(t, precheck.VerdictDrop, dec.Verdict)
	assert.Equal(t, "empty", dec.Source, "Decision.Source 비어있으면 Decider 가 Name() 으로 자동 채움")
}

// emptySourceSource 는 Decision.Source 를 비워두는 stub.
type emptySourceSource struct{}

func (emptySourceSource) Name() string { return "empty" }
func (emptySourceSource) Check(_ context.Context, _ string) precheck.Decision {
	return precheck.Decision{Verdict: precheck.VerdictDrop}
}
