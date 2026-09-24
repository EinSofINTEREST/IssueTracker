package bus_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
)

func jobAt(rawURL string) *core.CrawlJob {
	return &core.CrawlJob{Target: core.Target{URL: rawURL}}
}

// TestScoreResolver_EmptySnapshot_Delegates 는 스냅샷이 없으면 chain 에 위임하는지
// 검증합니다 — 부팅 직후 scorer 첫 주기 전까지는 기존 동작이 유지돼야 합니다.
func TestScoreResolver_EmptySnapshot_Delegates(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.7)
	assert.False(t, r.CanResolve(jobAt("https://a.example.com/x")))
}

// TestScoreResolver_AboveThreshold_High 는 threshold 이상이면 High 를 주는지 검증합니다.
func TestScoreResolver_AboveThreshold_High(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.7)
	r.SetScores(map[string]float64{"a.example.com": 0.8})

	job := jobAt("https://a.example.com/article/1")
	assert.True(t, r.CanResolve(job))
	assert.Equal(t, core.PriorityHigh, r.Resolve(job))
}

// TestScoreResolver_BelowThreshold_Normal 은 미달이면 Normal 인지 검증합니다.
//
// **Low 로 내리지 않는 것이 핵심이다.** 관찰 신호로 자동 강등하면 일시적 품질 저하가
// host 를 저우선으로 묶고, 그 상태에서 수집이 줄어 신호가 갱신되지 않는 되먹임이 생긴다.
func TestScoreResolver_BelowThreshold_Normal(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.7)
	r.SetScores(map[string]float64{"a.example.com": 0.1})

	job := jobAt("https://a.example.com/article/1")
	assert.True(t, r.CanResolve(job))
	assert.Equal(t, core.PriorityNormal, r.Resolve(job), "점수가 낮아도 Low 로 내리지 않는다")
}

// TestScoreResolver_ExactThreshold_High 는 경계값 포함 여부를 고정합니다.
func TestScoreResolver_ExactThreshold_High(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.7)
	r.SetScores(map[string]float64{"a.example.com": 0.7})
	assert.Equal(t, core.PriorityHigh, r.Resolve(jobAt("https://a.example.com/x")))
}

// TestScoreResolver_UnknownHost_Delegates 는 점수 없는 host 가 위임되는지 검증합니다.
func TestScoreResolver_UnknownHost_Delegates(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.7)
	r.SetScores(map[string]float64{"a.example.com": 0.9})
	assert.False(t, r.CanResolve(jobAt("https://other.example.com/x")))
}

// TestScoreResolver_HostNormalization 은 포트와 대소문자가 정규화되는지 검증합니다.
//
// 점수는 host 단위로 저장되므로 같은 host 가 포트 유무나 대소문자로 갈리면 조회가 빗나간다.
func TestScoreResolver_HostNormalization(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.5)
	r.SetScores(map[string]float64{"a.example.com": 0.9})

	for _, u := range []string{
		"https://a.example.com/x",
		"https://A.Example.COM/x",
		"http://a.example.com:8080/x",
	} {
		assert.True(t, r.CanResolve(jobAt(u)), "정규화 실패: %s", u)
	}
}

// TestScoreResolver_InvalidURL_Delegates 는 URL 파싱 실패 시 위임하는지 검증합니다.
func TestScoreResolver_InvalidURL_Delegates(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.5)
	r.SetScores(map[string]float64{"a.example.com": 0.9})

	assert.False(t, r.CanResolve(jobAt("://broken")))
	assert.False(t, r.CanResolve(jobAt("")))
	assert.False(t, r.CanResolve(nil))
}

// TestScoreResolver_SetScores_Empty_Disables 는 빈 맵이 resolver 를 비활성화하는지
// 검증합니다 — "점수 없음" 을 의미하는 명시적 신호입니다.
func TestScoreResolver_SetScores_Empty_Disables(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.5)
	r.SetScores(map[string]float64{"a.example.com": 0.9})
	assert.True(t, r.CanResolve(jobAt("https://a.example.com/x")))

	r.SetScores(nil)
	assert.False(t, r.CanResolve(jobAt("https://a.example.com/x")))
}

// TestScoreResolver_SetScores_CopiesInput 은 호출자가 맵을 수정해도 스냅샷이 흔들리지
// 않는지 검증합니다.
func TestScoreResolver_SetScores_CopiesInput(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.5)
	src := map[string]float64{"a.example.com": 0.9}
	r.SetScores(src)

	src["a.example.com"] = 0.0
	src["b.example.com"] = 0.9

	assert.Equal(t, core.PriorityHigh, r.Resolve(jobAt("https://a.example.com/x")))
	assert.False(t, r.CanResolve(jobAt("https://b.example.com/x")))
}

// TestScoreResolver_ConcurrentReadWrite 는 스냅샷 교체와 조회가 race-safe 한지
// 검증합니다 — Resolve 는 publish hot path 에서 호출된다.
func TestScoreResolver_ConcurrentReadWrite(t *testing.T) {
	r := bus.NewDynamicScorePriorityResolver(0.5)
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.Resolve(jobAt("https://a.example.com/x"))
				r.CanResolve(jobAt("https://a.example.com/x"))
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.SetScores(map[string]float64{"a.example.com": float64(n%2) * 0.9})
			}
		}(i)
	}
	wg.Wait()
}
