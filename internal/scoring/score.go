// Package scoring 은 host 단위 priority score 를 계산합니다 (이슈 #382, 메타 #380 Sub B).
//
// # 범위
//
// **High ↔ Normal 분기만** 결정합니다. Low 는 대상이 아닙니다 — Low 는 운영자가 DB
// `crawl_priority` 로 명시한 결과이고 (이슈 #521), 관찰 신호로 그 명시를 뒤집으면 운영자의
// 의도가 조용히 무시됩니다.
//
// # 왜 3 signal 인가
//
// 원 설계 (이슈 #382) 는 5개 signal 을 들었으나 `cost` / `reliability` 는 제외하고
// 시작합니다. 두 신호의 출처인 Prometheus metrics 는
//
//   - 앱에 읽기 경로가 없고 (`Gather()` 사용처 0),
//   - 필요한 것이 현재 값이 아니라 **구간 rate** 이며 (누적 counter 를 차분해야 함),
//   - 인스턴스 로컬이라 여러 인스턴스에서 host 점수가 갈립니다 (이슈 #289 와 얽힘).
//
// 골격을 먼저 세우고 그 결정이 난 뒤 얹는 편이 되돌리기 쉽습니다.
package scoring

import (
	"math"

	"issuetracker/internal/storage/model"
)

// Weights 는 signal 별 가중치입니다 (cluster-wide — per-host 는 이슈 #383).
//
// 합이 1 일 필요는 없습니다. Score 가 결과를 [0,1] 로 clamp 합니다.
type Weights struct {
	Freshness float64
	Impact    float64
	HostTrust float64
}

// DefaultWeights 는 운영 기본값입니다.
//
// 근거: host_trust 를 가장 크게 둔다 — 세 신호 중 유일하게 **결과 품질** 을 직접 반영하고,
// 표본이 쌓일수록 안정적이다. freshness 는 시의성 신호로 다음. impact 는 카테고리 가중치라
// host 단위로는 변별력이 가장 낮다 (같은 host 안에서도 기사마다 다름).
var DefaultWeights = Weights{
	Freshness: 0.3,
	Impact:    0.2,
	HostTrust: 0.5,
}

// MinSampleCount 는 점수를 신뢰하기 위한 최소 표본 수입니다.
//
// 표본이 적은 점수를 그대로 쓰면 노이즈가 우선순위에 반영됩니다 — 기사 2건으로 계산된
// host_trust 1.0 이 High 로 올라가는 식입니다. 미만이면 cold-start 로 보고 점수를 내지
// 않아 chain 의 기존 경로에 위임합니다.
const MinSampleCount = 20

// Score 는 signal 을 가중 합산해 [0,1] 점수를 냅니다.
//
// 표본이 MinSampleCount 미만이면 (0, false) — 호출자는 점수를 저장하지 않고 cold-start 로
// 취급해야 합니다.
func Score(s model.HostSignals, w Weights) (float64, bool) {
	if s.SampleCount < MinSampleCount {
		return 0, false
	}

	total := w.Freshness + w.Impact + w.HostTrust
	if total <= 0 {
		// weight 가 전부 0 이면 점수에 의미가 없다. 조용히 0 을 돌려주면 모든 host 가
		// Normal 로 고정되므로, 설정 실수임을 호출자가 알 수 있게 false 를 반환한다.
		return 0, false
	}

	raw := w.Freshness*clamp01(s.Freshness) +
		w.Impact*clamp01(s.Impact) +
		w.HostTrust*clamp01(s.HostTrust)

	return clamp01(raw / total), true
}

// clamp01 은 값을 [0,1] 로 제한합니다. NaN 은 0 으로 취급합니다.
//
// NaN 을 0 으로 내리는 이유: 0 나눗셈 등으로 생긴 NaN 이 비교 연산에서 항상 false 가 되어
// threshold 판정이 조용히 Normal 로 떨어집니다. 명시적으로 0 을 주는 편이 추적하기 쉽습니다.
func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}

// MergeWeights 는 부분 지정된 host weight 를 **base (운영 중인 cluster-wide 값) 위에**
// 덮습니다 (이슈 #383).
//
// 세 값이 모두 nil 이면 nil 을 반환합니다 — 조정 없음.
//
// base 를 인자로 받는 것이 핵심이다. DefaultWeights 를 쓰면 운영자가 cluster weight 를
// 바꿔 놨어도 **부분 지정한 host 만 그 설정을 잃고** 하드코딩 기본값으로 떨어진다 —
// 설정이 조용히 무시되는 종류다.
func MergeWeights(base Weights, freshness, impact, hostTrust *float64) *Weights {
	if freshness == nil && impact == nil && hostTrust == nil {
		return nil
	}
	out := base
	if freshness != nil {
		out.Freshness = *freshness
	}
	if impact != nil {
		out.Impact = *impact
	}
	if hostTrust != nil {
		out.HostTrust = *hostTrust
	}
	return &out
}
