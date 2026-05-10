package pathinfer_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/pathinfer"
)

// validateBroadness 는 unexported 이지만 InferLLM 의 마지막 검증 단계로 entry — 본 테스트는
// InferLLM 을 호출하여 (LLM 응답 = pattern) end-to-end 로 broadness 검증 동작을 확인합니다.
//
// 모든 테스트는 PATHINFER_BROADNESS_CHECK 미설정 (default true) 가정 — t.Setenv 로 회피.

func newBroadnessLLM(pattern string) *fakeLLM {
	return &fakeLLM{resp: pattern}
}

func TestBroadness_AcceptsSpecificPattern(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/200", "/article/9999"},
	}
	llm := newBroadnessLLM(`^/article/\d+$`)
	pattern, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.True(t, ok, "리터럴 + 좁은 capture — 통과")
	assert.Equal(t, `^/article/\d+$`, pattern)
}

func TestBroadness_RejectsBroadFirstSegment(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/200", "/article/9999"},
	}
	llm := newBroadnessLLM(`^/.+/\d+$`)
	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.False(t, ok, "첫 segment .+ 로 broad — 거부")
}

func TestBroadness_RejectsBracketBroadSegment(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/200", "/article/9999"},
	}
	llm := newBroadnessLLM(`^/[^/]+/\d+$`)
	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.False(t, ok, "첫 segment [^/]+ — broadnesstoken 매칭 → 거부")
}

func TestBroadness_RejectsLowercaseBroadSegment(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/200", "/article/9999"},
	}
	// [a-z]+ 는 broadnesstoken (모두 소문자) 도 매칭 → broad 판정.
	llm := newBroadnessLLM(`^/[a-z]+/\d+$`)
	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.False(t, ok, "literal 자리에 [a-z]+ — token 매칭 → 거부")
}

func TestBroadness_AcceptsVariedFirstSegment(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	// positive 가 다양한 첫 segment 를 가지면 그 자리는 constant 가 아니므로 [a-z]+ 통과.
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/news/2", "/column/3"},
	}
	llm := newBroadnessLLM(`^/[a-z]+/\d+$`)
	pattern, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.True(t, ok, "다양한 첫 segment — broadness 검증 skip")
	assert.Equal(t, `^/[a-z]+/\d+$`, pattern)
}

func TestBroadness_RejectsTooManySegments(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/200", "/article/9999"},
	}
	// 3 segment 까지 허용하는 pattern — positive 는 모두 2 segment 인데 K+1 매칭 → broad.
	llm := newBroadnessLLM(`^/article/\d+(/.+)?$`)
	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.False(t, ok, "K+1 segment 매칭 → 거부")
}

func TestBroadness_AcceptsMatchingSegmentCount(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	samples := pathinfer.LLMSamples{
		Articles: []string{"/news/100", "/news/200", "/news/300"},
	}
	llm := newBroadnessLLM(`^/news/\d+$`)
	pattern, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, `^/news/\d+$`, pattern)
}

func TestBroadness_EnvToggleDisables(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "false")
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/200", "/article/9999"},
	}
	// 정상 정밀화도 거부될 수 있는 broad 패턴 — toggle off 상태에서는 통과.
	llm := newBroadnessLLM(`^/[^/]+/\d+$`)
	pattern, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.True(t, ok, "PATHINFER_BROADNESS_CHECK=false — 휴리스틱 skip")
	assert.Equal(t, `^/[^/]+/\d+$`, pattern)
}

func TestBroadness_EnvToggleVariations(t *testing.T) {
	cases := []struct {
		env     string
		enabled bool
	}{
		{"", true},        // 미설정 = default true
		{"true", true},    // 명시 true
		{"1", true},       // 1 도 true 로 취급
		{"yes", true},     //
		{"false", false},  //
		{"FALSE", false},  // case-insensitive
		{"0", false},      //
		{"no", false},     //
		{"  no  ", false}, // trim
	}
	for _, c := range cases {
		c := c
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("PATHINFER_BROADNESS_CHECK", c.env)
			samples := pathinfer.LLMSamples{
				Articles: []string{"/article/1", "/article/200", "/article/9999"},
			}
			llm := newBroadnessLLM(`^/[^/]+/\d+$`)
			_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
			require.NoError(t, err)
			if c.enabled {
				assert.False(t, ok, "broadness 활성 — broad 패턴 거부")
			} else {
				assert.True(t, ok, "broadness 비활성 — broad 패턴 통과")
			}
		})
	}
}

func TestBroadness_SinglePositiveSkipsLiteralCheck(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	// positive 가 1개면 literal segment check skip — 정상 capture (\d+) 도 거부될 위험 회피.
	// 단 minSamples (3) 미만이면 InferLLM 자체가 일찍 반환 — 본 테스트는 minSamples=1 override.
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1"},
	}
	llm := newBroadnessLLM(`^/article/\d+$`)
	pattern, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader, pathinfer.WithMinSamples(1))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, `^/article/\d+$`, pattern)
}

func TestBroadness_VariedSegmentCountsSkipsValidation(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	// segment 수가 들쑥날쑥 — broadness 검증 skip (fail-open).
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/sub/2", "/article/3"},
	}
	// catch-all 보다 약간만 좁은 패턴 — 정상이면 거부되겠지만 segment 수 mismatch 로 skip.
	llm := newBroadnessLLM(`^/article(/.+)?$`)
	pattern, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.True(t, ok, "segment 수 들쑥날쑥 — fail-open")
	assert.Equal(t, `^/article(/.+)?$`, pattern)
}

func TestBroadness_RejectsSinglePathFollowedByTrailingSlash(t *testing.T) {
	t.Setenv("PATHINFER_BROADNESS_CHECK", "")
	// K-1 변형: 마지막 segment 제거 후 매칭되면 broad.
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/2", "/article/3"},
	}
	// /article 도 매칭하는 패턴 — 1 segment 변형 매칭 → broad.
	llm := newBroadnessLLM(`^/article(/\d+)?$`)
	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, testLoader)
	require.NoError(t, err)
	assert.False(t, ok, "K-1 변형 매칭 → 거부")
}
