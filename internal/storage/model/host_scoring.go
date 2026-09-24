package model

import (
	"encoding/json"
	"time"
)

// HostScoringState 는 host 단위 동적 priority score 의 저장 형태입니다 (이슈 #382).
//
// score 는 [0,1] 정규화 값이며 **High ↔ Normal 분기에만** 쓰입니다. Low 후보는 본 scoring
// 대상이 아닙니다 — Low 는 운영자가 DB `crawl_priority` 로 명시한 결과이고, 관찰 신호로
// 그 명시를 뒤집으면 운영자의 의도가 조용히 무시됩니다.
type HostScoringState struct {
	Host string

	// Score 는 집계된 정규화 점수. threshold 이상이면 High.
	Score float64

	// Signals 는 입력값 스냅샷입니다.
	//
	// 점수만 남기면 "왜 그 점수인지" 를 사후에 알 수 없어 운영자가 weight 를 조정할 근거가
	// 사라집니다. 스키마를 고정하지 않는 이유는 signal 집합이 아직 확장 중이기 때문입니다
	// (cost / reliability 가 이후 추가 예정 — 이슈 #382 참조).
	Signals json.RawMessage

	// WindowMinutes 는 집계 구간입니다. weight 를 바꿔 재계산할 때 같은 구간인지 확인용.
	WindowMinutes int

	CalculatedAt time.Time
}

// HostSignals 는 점수 계산에 들어간 입력값입니다 (이슈 #382 — 3 signal 로 시작).
//
// 각 값은 [0,1] 정규화됩니다. 정규화 책임은 계산기 쪽에 있으며, 본 struct 는 운반과
// 로깅/디버깅용 직렬화만 담당합니다.
type HostSignals struct {
	// Freshness: publish → detect lag 이 짧을수록 1 에 가깝습니다.
	Freshness float64 `json:"freshness"`

	// Impact: 카테고리 가중치. 정치/경제 등이 높습니다.
	Impact float64 `json:"impact"`

	// HostTrust: validation 통과율 기반 누적 신뢰도.
	HostTrust float64 `json:"host_trust"`

	// SampleCount 는 집계에 쓰인 문서 수입니다.
	//
	// 점수 자체에는 들어가지 않지만 **cold-start 판정의 근거** 라 함께 남깁니다 —
	// 표본이 적은 점수를 신뢰해 우선순위를 올리면 노이즈가 그대로 반영됩니다.
	SampleCount int `json:"sample_count"`
}
