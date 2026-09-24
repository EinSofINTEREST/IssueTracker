package bus_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestOverride_EmptySnapshot_Delegates 는 스냅샷이 없으면 chain 에 위임하는지 검증합니다.
func TestOverride_EmptySnapshot_Delegates(t *testing.T) {
	r := bus.NewOverridePriorityResolver()
	assert.False(t, r.CanResolve(jobAt("https://a.example.com/x")))
}

// TestOverride_Indefinite_Applies 는 만료 시각이 없으면 무기한 적용되는지 검증합니다.
func TestOverride_Indefinite_Applies(t *testing.T) {
	r := bus.NewOverridePriorityResolver()
	r.SetOverrides(map[string]bus.HostOverride{
		"a.example.com": {Priority: core.PriorityHigh},
	})

	job := jobAt("https://a.example.com/x")
	assert.True(t, r.CanResolve(job))
	assert.Equal(t, core.PriorityHigh, r.Resolve(job))
}

// TestOverride_Expired_Delegates 는 만료된 override 가 위임되는지 검증합니다.
//
// **만료 판정이 조회 시점이라는 것이 핵심이다.** 적재 시점에 걸렀다면 refresh 주기 동안
// 만료된 값이 계속 적용된다 — 스냅샷을 바꾸지 않고 시계만 넘겨 그 점을 확인한다.
func TestOverride_Expired_Delegates(t *testing.T) {
	until := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	r := bus.NewOverridePriorityResolver()
	r.SetOverrides(map[string]bus.HostOverride{
		"a.example.com": {Priority: core.PriorityHigh, Until: until},
	})

	job := jobAt("https://a.example.com/x")

	r.SetClock(fixedClock(until.Add(-time.Minute)))
	assert.True(t, r.CanResolve(job), "만료 전에는 적용된다")

	// 스냅샷은 그대로 두고 시각만 넘긴다.
	r.SetClock(fixedClock(until.Add(time.Minute)))
	assert.False(t, r.CanResolve(job), "만료 후에는 refresh 없이도 즉시 해제돼야 한다")
}

// TestOverride_ExactBoundary_Expired 는 만료 경계 포함 여부를 고정합니다.
func TestOverride_ExactBoundary_Expired(t *testing.T) {
	until := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	r := bus.NewOverridePriorityResolver()
	r.SetOverrides(map[string]bus.HostOverride{
		"a.example.com": {Priority: core.PriorityHigh, Until: until},
	})
	r.SetClock(fixedClock(until))
	assert.False(t, r.CanResolve(jobAt("https://a.example.com/x")),
		"until 시점은 이미 만료로 본다 (Before 비교)")
}

// TestOverride_LowIsAllowed 는 override 가 Low 로도 내릴 수 있는지 검증합니다.
//
// scoring 과 다른 점이다 — scoring 은 관찰 신호라 Low 강등을 하지 않지만, override 는
// 운영자의 명시적 결정이므로 Low 가 허용돼야 한다.
func TestOverride_LowIsAllowed(t *testing.T) {
	r := bus.NewOverridePriorityResolver()
	r.SetOverrides(map[string]bus.HostOverride{
		"a.example.com": {Priority: core.PriorityLow},
	})
	assert.Equal(t, core.PriorityLow, r.Resolve(jobAt("https://a.example.com/x")))
}

// TestOverride_UnknownHost_Delegates 는 설정 없는 host 가 위임되는지 검증합니다.
func TestOverride_UnknownHost_Delegates(t *testing.T) {
	r := bus.NewOverridePriorityResolver()
	r.SetOverrides(map[string]bus.HostOverride{"a.example.com": {Priority: core.PriorityHigh}})
	assert.False(t, r.CanResolve(jobAt("https://other.example.com/x")))
}

// TestOverride_HostNormalization 은 포트·대소문자가 정규화되는지 검증합니다.
func TestOverride_HostNormalization(t *testing.T) {
	r := bus.NewOverridePriorityResolver()
	r.SetOverrides(map[string]bus.HostOverride{"a.example.com": {Priority: core.PriorityHigh}})

	for _, u := range []string{
		"https://A.Example.COM/x",
		"http://a.example.com:8080/x",
	} {
		assert.True(t, r.CanResolve(jobAt(u)), "정규화 실패: %s", u)
	}
}

// TestOverride_SetOverrides_CopiesInput 은 호출자의 맵 수정이 스냅샷에 새지 않는지
// 검증합니다.
func TestOverride_SetOverrides_CopiesInput(t *testing.T) {
	r := bus.NewOverridePriorityResolver()
	src := map[string]bus.HostOverride{"a.example.com": {Priority: core.PriorityHigh}}
	r.SetOverrides(src)

	src["a.example.com"] = bus.HostOverride{Priority: core.PriorityLow}
	assert.Equal(t, core.PriorityHigh, r.Resolve(jobAt("https://a.example.com/x")))
}

// ── per-host threshold (score resolver 확장) ────────────────────────────────

// TestScoreResolver_HostThreshold_Overrides 는 host 별 임계값이 기본값을 덮는지
// 검증합니다.
func TestScoreResolver_HostThreshold_Overrides(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.7)
	r.SetScores(map[string]float64{"a.example.com": 0.6, "b.example.com": 0.6})
	r.SetHostThresholds(map[string]float64{"a.example.com": 0.5})

	assert.Equal(t, core.PriorityHigh, r.Resolve(jobAt("https://a.example.com/x")),
		"host 임계값 0.5 미만이 아니므로 High")
	assert.Equal(t, core.PriorityNormal, r.Resolve(jobAt("https://b.example.com/x")),
		"기본 임계값 0.7 미달이므로 Normal")
}

// TestScoreResolver_HostThreshold_IndependentSnapshot 은 점수와 임계값 갱신이 서로를
// 덮지 않는지 검증합니다.
//
// 두 값은 갱신 주기가 다르다 — 점수는 scorer 집계 주기, 임계값은 운영자 설정 refresh 주기.
// 한 맵에 묶으면 한쪽 갱신이 다른 쪽을 지운다.
func TestScoreResolver_HostThreshold_IndependentSnapshot(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.9)
	r.SetHostThresholds(map[string]float64{"a.example.com": 0.5})
	r.SetScores(map[string]float64{"a.example.com": 0.6})

	assert.Equal(t, core.PriorityHigh, r.Resolve(jobAt("https://a.example.com/x")),
		"점수 갱신이 임계값 스냅샷을 지우면 안 된다")

	r.SetScores(map[string]float64{"a.example.com": 0.4})
	assert.Equal(t, core.PriorityNormal, r.Resolve(jobAt("https://a.example.com/x")))
}
