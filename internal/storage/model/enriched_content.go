package model

import "time"

// EnrichedContentRecord 는 enriched_contents 테이블의 row 표현입니다 (이슈 #450).
//
// JSONB 필드 (Facts / Verifications / Context / Factors) 는 raw byte slice 로 보관 — DB 와
// wire format 양쪽에서 동일하게 다루기 위함. 호출자가 필요 시 json.Unmarshal 로 강타입 구조체로 변환.
//
// Rationale / Factors 는 이슈 #457 (migration 030) 으로 추가된 scorer 진단 필드.
type EnrichedContentRecord struct {
	ID            int64
	ContentID     string
	TrustScore    float64
	Facts         []byte // JSONB
	Verifications []byte // JSONB
	Context       []byte // JSONB
	Rationale     string // TEXT — scorer 가 산출한 trust_score 근거 텍스트 (이슈 #457)
	Factors       []byte // JSONB — {claim_support_ratio, source_diversity, context_completeness} (이슈 #457)
	EnrichedAt    time.Time
}
