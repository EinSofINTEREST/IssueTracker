package model

import "time"

// SampleURL 은 parser_rule_sample_urls 테이블의 단일 행입니다.
//
// SampleURL represents a single accumulated sample URL for a parsing rule —
// used by the progressive refinement workflow (path_pattern 추론).
type SampleURL struct {
	ID         int64
	RuleID     int64
	URL        string
	ObservedAt time.Time
}

// SampleCapPerRule 은 같은 rule_id 의 sample 누적 상한입니다.
//
// 단계 4-2 의 trigger 가 5개 시점에 정밀화 + purge 하면 본 cap 에 도달하지 않음.
// trigger 미동작 / LLM_ENABLED=false 등으로 정밀화가 발생하지 않을 때 DB 폭증 방어.
const SampleCapPerRule = 100
