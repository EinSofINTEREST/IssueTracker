package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PriorityConfig 는 host 단위 priority 운영 설정입니다 (이슈 #383, 메타 #380 Sub C).
//
// `fetcher_rules.priority_config` JSONB 에 저장됩니다. **모든 필드가 optional** 이며 부분
// override 가 가능합니다 — 예를 들어 threshold 만 지정하고 weight 는 cluster-wide 기본값을
// 쓰는 구성이 성립합니다.
type PriorityConfig struct {
	// BasePriority 는 명시적 override 입니다 ("high" / "normal" / "low").
	//
	// 가장 강한 신호 — 설정되면 scoring 을 건너뜁니다. 운영자가 관찰 결과와 무관하게
	// 특정 host 를 고정하고 싶을 때 씁니다.
	BasePriority string `json:"base_priority,omitempty"`

	// OverrideUntil 은 BasePriority 의 만료 시각입니다 (RFC3339).
	//
	// 없으면 무기한입니다. **만료 판정은 적재 시점이 아니라 조회 시점에** 합니다 —
	// 적재 시점에 거르면 refresh interval 동안 이미 만료된 override 가 계속 적용됩니다.
	OverrideUntil *time.Time `json:"override_until,omitempty"`

	// SignalWeights 는 이 host 의 scoring weight 입니다. 미지정 키는 cluster-wide 기본값.
	SignalWeights *SignalWeights `json:"signal_weights,omitempty"`

	// ScoreThreshold 는 이 host 의 High 분기 임계값입니다. nil 이면 cluster-wide 기본값.
	ScoreThreshold *float64 `json:"score_threshold,omitempty"`
}

// SignalWeights 는 host 별 signal 가중치입니다.
//
// Sub B (이슈 #382) 가 실제로 쓰는 **3개 signal 만** 둡니다. 원 설계의 cost / reliability 는
// Sub B 에서 제외됐으므로 (metrics 읽기 경로 부재 + 인스턴스 로컬 문제) 지금 필드로 두면
// **운영자가 설정해도 조용히 무시되는 키** 가 됩니다. 필요해지면 그때 추가합니다.
type SignalWeights struct {
	Freshness *float64 `json:"freshness,omitempty"`
	Impact    *float64 `json:"impact,omitempty"`
	HostTrust *float64 `json:"host_trust,omitempty"`
}

// ParsePriorityConfig 는 JSONB raw 를 PriorityConfig 로 파싱하고 검증합니다.
//
// raw 가 비어 있으면 (nil, nil) — override 없음이 정상 상태입니다.
//
// 알 수 없는 키는 **거부합니다.** 오타난 키를 조용히 무시하면 운영자는 설정이 적용된 줄
// 알지만 동작은 그대로이고, 그 사실이 어디에도 드러나지 않습니다.
func ParsePriorityConfig(raw []byte) (*PriorityConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()

	var cfg PriorityConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse priority_config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate 는 값의 유효성을 확인합니다.
func (c *PriorityConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.BasePriority != "" {
		switch strings.ToLower(c.BasePriority) {
		case "high", "normal", "low":
		default:
			return fmt.Errorf("invalid base_priority %q (expected high|normal|low)", c.BasePriority)
		}
	}
	// override_until 만 있고 base_priority 가 없으면 만료시킬 대상이 없다 — 설정 실수다.
	if c.OverrideUntil != nil && c.BasePriority == "" {
		return fmt.Errorf("override_until set without base_priority")
	}
	if c.ScoreThreshold != nil && (*c.ScoreThreshold < 0 || *c.ScoreThreshold > 1) {
		return fmt.Errorf("invalid score_threshold %v (must be within [0,1])", *c.ScoreThreshold)
	}
	for name, w := range map[string]*float64{
		"freshness":  weightOf(c.SignalWeights, func(s *SignalWeights) *float64 { return s.Freshness }),
		"impact":     weightOf(c.SignalWeights, func(s *SignalWeights) *float64 { return s.Impact }),
		"host_trust": weightOf(c.SignalWeights, func(s *SignalWeights) *float64 { return s.HostTrust }),
	} {
		if w != nil && *w < 0 {
			return fmt.Errorf("invalid signal_weights.%s %v (must not be negative)", name, *w)
		}
	}
	return nil
}

func weightOf(s *SignalWeights, pick func(*SignalWeights) *float64) *float64 {
	if s == nil {
		return nil
	}
	return pick(s)
}

// OverrideActive 는 지정 시각 기준으로 BasePriority override 가 유효한지 반환합니다.
//
// now 를 인자로 받는 이유: 만료 경계를 테스트할 수 있어야 하고, 호출자가 한 번 읽은
// 시각으로 여러 host 를 일관되게 판정할 수 있습니다.
func (c *PriorityConfig) OverrideActive(now time.Time) bool {
	if c == nil || c.BasePriority == "" {
		return false
	}
	if c.OverrideUntil == nil {
		return true // 무기한
	}
	return now.Before(*c.OverrideUntil)
}
