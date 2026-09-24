package bus

import (
	"net/url"
	"strings"
	"sync/atomic"

	"issuetracker/internal/processor/fetcher/core"
)

// DynamicScorePriorityResolver 는 host 점수로 High ↔ Normal 을 분기합니다 (이슈 #382).
//
// # 체인 위치
//
// RuleBasedPriorityResolver **뒤** 에 둡니다. 운영자가 DB `crawl_priority` 로 명시한 host 는
// 그쪽에서 확정되고, 본 resolver 는 **명시가 없어 Normal 로 흐르던 host** 만 다룹니다.
// 앞에 두면 관찰 신호가 운영자의 명시를 뒤집게 되어 의도가 조용히 무시됩니다.
//
// # Low 를 만들지 않는다
//
// 점수가 낮아도 Normal 까지만 내려갑니다. Low 강등은 운영자의 명시적 결정이어야 합니다 —
// 관찰 신호로 자동 강등하면 일시적 품질 저하가 host 를 저우선으로 묶어버리고, 그 상태에서
// 수집이 줄어 신호가 갱신되지 않는 되먹임이 생깁니다.
//
// # 스냅샷 방식
//
// Resolve 는 publish hot path 에서 호출되므로 host 마다 DB 를 왕복할 수 없습니다.
// scorer goroutine 이 주기적으로 전량을 읽어 atomic 으로 교체합니다 — 동시 Resolve 호출과
// race-safe 합니다 (RuleBasedPriorityResolver.SetHostPathRules 와 같은 패턴).
type DynamicScorePriorityResolver struct {
	scores    atomic.Pointer[map[string]float64]
	threshold float64
}

// NewDynamicScorePriorityResolver 는 threshold 를 지정해 생성합니다.
//
// 스냅샷이 주입되기 전에는 CanResolve 가 항상 false — 부팅 직후 scorer 가 첫 주기를 돌기
// 전까지는 기존 chain 동작이 그대로 유지됩니다.
func NewDynamicScorePriorityResolver(threshold float64) *DynamicScorePriorityResolver {
	return &DynamicScorePriorityResolver{threshold: threshold}
}

// SetScores 는 host → score 스냅샷을 atomic 으로 교체합니다.
//
// nil 또는 빈 맵을 주면 resolver 가 비활성화됩니다 (CanResolve=false → chain 위임).
// scorer 가 집계에 실패했을 때 **이전 스냅샷을 지우지 말아야** 한다면 호출하지 않으면 됩니다 —
// 빈 맵 전달은 "점수 없음" 을 의미하는 명시적 신호입니다.
func (r *DynamicScorePriorityResolver) SetScores(scores map[string]float64) {
	if len(scores) == 0 {
		r.scores.Store(nil)
		return
	}
	// 호출자가 이후에 맵을 수정해도 스냅샷이 흔들리지 않도록 복사한다.
	cp := make(map[string]float64, len(scores))
	for k, v := range scores {
		cp[k] = v
	}
	r.scores.Store(&cp)
}

// Resolve 는 host 점수가 threshold 이상이면 High, 그 외에는 Normal 을 반환합니다.
func (r *DynamicScorePriorityResolver) Resolve(job *core.CrawlJob) core.Priority {
	score, ok := r.lookup(job)
	if !ok {
		return core.PriorityNormal
	}
	if score >= r.threshold {
		return core.PriorityHigh
	}
	return core.PriorityNormal
}

// CanResolve 는 job 의 host 에 대한 점수가 있는 경우에만 true 를 반환합니다.
//
// 점수가 없는 host (cold-start / 신규) 는 체인의 다음 resolver 로 위임합니다.
func (r *DynamicScorePriorityResolver) CanResolve(job *core.CrawlJob) bool {
	_, ok := r.lookup(job)
	return ok
}

func (r *DynamicScorePriorityResolver) lookup(job *core.CrawlJob) (float64, bool) {
	if job == nil {
		return 0, false
	}
	m := r.scores.Load()
	if m == nil {
		return 0, false
	}
	host := hostOf(job.Target.URL)
	if host == "" {
		return 0, false
	}
	score, ok := (*m)[host]
	return score, ok
}

// hostOf 는 URL 에서 host 를 추출합니다 — 포트를 떼고 소문자로 정규화합니다.
//
// 점수는 host 단위로 저장되므로 같은 host 가 포트 유무나 대소문자로 갈리면 조회가 빗나갑니다.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	h := u.Hostname()
	if h == "" {
		return ""
	}
	return strings.ToLower(h)
}
