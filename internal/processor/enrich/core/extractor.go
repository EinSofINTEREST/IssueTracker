// Package extractor 는 enrich 단계의 LLM 추출기 인터페이스 + 구현체를 제공합니다 (이슈 #447).
//
// Extractor 는 page 메타데이터를 받아 EnrichedFacts 를 반환하는 단일 메소드 인터페이스입니다.
// 구현체:
//   - NoopExtractor: 빈 EnrichedFacts 반환. claudegen 미configured / disabled 환경의 fallback.
//   - ClaudegenExtractor: claude.Pool 을 통해 Claude Code 호출 (실제 추출).
package extractor

import (
	"context"
)

// Input 은 Extractor.Extract 입력 — page 식별 + 본문.
type Input struct {
	// URL 은 page 의 canonical URL — prompt 에 직접 주입되어 claudegen 이 검증 등에 사용.
	URL string
	// Host 는 URL 의 host portion (예: cnn.com). prompt 에 주입.
	Host string
	// Title 은 페이지 제목 (선택) — prompt 에 주입되어 context 제공.
	Title string
	// HTML 은 page 본문 (parser 가 추출한 정제된 텍스트 또는 raw HTML).
	// claudegen container 의 세션 디렉토리에 page.html 파일로 기록됩니다.
	HTML string
}

// Extractor 는 page 입력으로부터 EnrichedFacts 를 추출합니다.
//
// 호출자 (enrich worker) 는 ctx timeout 을 적용하여 호출. error 반환 시 worker 는 빈 facts 로
// fallback 하고 forward 를 계속 진행 — enrichment 실패가 파이프라인을 멈추지 않도록.
type Extractor interface {
	Extract(ctx context.Context, in Input) (*EnrichedFacts, error)
}
