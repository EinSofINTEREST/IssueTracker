package model

import "time"

// EnrichedContentRecord 는 enriched_contents 테이블의 row 표현입니다 (이슈 #450).
//
// JSONB 필드 (Facts / Verifications / Context) 는 raw byte slice 로 보관 — DB 와 wire format
// 양쪽에서 동일하게 다루기 위함. 호출자가 필요 시 json.Unmarshal 로 강타입 구조체로 변환.
type EnrichedContentRecord struct {
	ID            int64
	ContentID     string
	TrustScore    float64
	Facts         []byte // JSONB
	Verifications []byte // JSONB
	Context       []byte // JSONB
	EnrichedAt    time.Time
}
