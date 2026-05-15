package core

import (
	"context"
)

// NoopExtractor 는 빈 EnrichedFacts 를 반환하는 placeholder 입니다.
//
// 용도: claudegen 비활성 환경 / 테스트 / 단계적 rollout 첫 단계에서 사용. enrich worker 는
// 항상 Extractor 의존을 받으므로 nil 대신 NoopExtractor 를 주입 → nil-check 분기 제거.
type NoopExtractor struct{}

// NewNoopExtractor 는 NoopExtractor 인스턴스를 반환합니다.
func NewNoopExtractor() *NoopExtractor {
	return &NoopExtractor{}
}

// Extract 는 항상 빈 (zero-value 필드) EnrichedFacts 를 반환합니다.
func (e *NoopExtractor) Extract(_ context.Context, _ Input) (*EnrichedFacts, error) {
	return &EnrichedFacts{
		Entities:  []Entity{},
		Claims:    []Claim{},
		Facts:     []Fact{},
		Topics:    []string{},
		Sentiment: SentimentNeutral,
	}, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ Extractor = (*NoopExtractor)(nil)
