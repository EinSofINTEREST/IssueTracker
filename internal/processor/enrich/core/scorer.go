// 본 파일은 enrich 단계의 신뢰도 점수 산출기 (Scorer) 를 정의합니다 (이슈 #450).
//
// Scorer 는 추출 + 검증 + 외부 맥락 결과를 종합하여 page 단위 0.0 ~ 1.0 신뢰도 점수와
// rationale / factors 를 반환합니다. claudegen 의 LLM 추론을 활용.

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"issuetracker/pkg/llm/prompt"
)

// TrustScoreResult 는 Scorer.Score 의 출력입니다.
//
// TrustScore 는 [0.0, 1.0] — 호출자 (worker) 가 EnrichedFacts.TrustScore 에 첨부 +
// DB enriched_contents.trust_score 에 영속화.
// Rationale / Factors 는 진단·운영 가시성용 메타데이터 — 본 sub-issue 범위에서는 metadata 첨부만,
// DB 영속화는 schema 단순화를 위해 생략 (필요 시 후속 schema evolution).
type TrustScoreResult struct {
	TrustScore float64
	Rationale  string
	Factors    ScoreFactors
}

// ScoreFactors 는 trust_score 구성 진단 필드들입니다.
type ScoreFactors struct {
	ClaimSupportRatio   float64 `json:"claim_support_ratio"`
	SourceDiversity     float64 `json:"source_diversity"`
	ContextCompleteness float64 `json:"context_completeness"`
}

// ScoreInput 은 Scorer.Score 의 입력입니다.
type ScoreInput struct {
	URL           string
	Host          string
	Title         string
	Facts         []byte // JSON 직렬화된 facts (entities/claims/...)
	Verifications []byte // JSON 직렬화된 verifications
	Context       []byte // JSON 직렬화된 context
}

// Scorer 는 종합 신뢰도 점수를 산출합니다.
//
// 실패 시 worker 가 score 미첨부 + DB write skip 으로 fallback — forward 보장.
type Scorer interface {
	Score(ctx context.Context, in ScoreInput) (*TrustScoreResult, error)
}

// NoopScorer 는 항상 (nil, nil) 을 반환합니다 — claudegen 미configured fallback.
type NoopScorer struct{}

// NewNoopScorer 는 NoopScorer 인스턴스를 반환합니다.
func NewNoopScorer() *NoopScorer { return &NoopScorer{} }

// Score 는 항상 (nil, nil) 을 반환합니다 — 점수 미첨부.
func (s *NoopScorer) Score(_ context.Context, _ ScoreInput) (*TrustScoreResult, error) {
	return nil, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ Scorer = (*NoopScorer)(nil)

// scorerPromptName 은 신뢰도 점수 prompt asset 경로입니다.
const scorerPromptName = "enrich/claude/score.user"

// ClaudegenScorer 는 claude.Pool 로 scoring session 을 실행합니다.
type ClaudegenScorer struct {
	runner SessionRunner
	loader prompt.Loader
}

// NewClaudegenScorer 는 claudegen-backed Scorer 를 생성합니다.
func NewClaudegenScorer(runner SessionRunner, loader prompt.Loader) (*ClaudegenScorer, error) {
	if runner == nil {
		return nil, errors.New("enrich/core: agent runner must not be nil")
	}
	if loader == nil {
		return nil, errors.New("enrich/core: prompt loader must not be nil")
	}
	return &ClaudegenScorer{runner: runner, loader: loader}, nil
}

// Score 는 claudegen 세션을 실행하고 stdout 을 TrustScoreResult 로 파싱합니다.
//
// 모든 입력 (facts/verifications/context) 이 빈 JSON 이면 runner skip — 점수 산출 근거 부족.
func (s *ClaudegenScorer) Score(ctx context.Context, in ScoreInput) (*TrustScoreResult, error) {
	if isEmptyJSON(in.Facts) && isEmptyJSON(in.Verifications) && isEmptyJSON(in.Context) {
		return nil, nil
	}

	tpl, err := s.loader.Load(scorerPromptName)
	if err != nil {
		return nil, fmt.Errorf("load scorer prompt %q: %w", scorerPromptName, err)
	}

	promptText := prompt.Render(tpl,
		"{{URL}}", in.URL,
		"{{HOST}}", in.Host,
		"{{TITLE}}", in.Title,
		"{{FACTS_JSON}}", jsonOrEmptyObject(in.Facts),
		"{{VERIFICATIONS_JSON}}", jsonOrEmptyArray(in.Verifications),
		"{{CONTEXT_JSON}}", jsonOrEmptyObject(in.Context),
	)

	stdout, err := s.runner.RunSession(ctx, "enrich-score", nil, promptText)
	if err != nil {
		return nil, fmt.Errorf("claudegen score session: %w", err)
	}

	res, err := parseScoreOutput(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse score output: %w", err)
	}
	return res, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ Scorer = (*ClaudegenScorer)(nil)

// scoreResponse 는 claudegen scorer 응답의 wire format 입니다.
//
// TrustScore 는 pointer 타입 — 응답에서 필드가 누락되면 0 으로 fallback 되지 않고 nil 로 잡혀
// 명시적 missing 검출 가능 (coderabbit-review PR #455). DB 에 fabricated 0 점수가 저장되는
// 사고 회피.
type scoreResponse struct {
	TrustScore *float64     `json:"trust_score"`
	Rationale  string       `json:"rationale"`
	Factors    ScoreFactors `json:"factors"`
}

// parseScoreOutput 는 claudegen stdout JSON 을 TrustScoreResult 로 파싱합니다.
//
// 필수 필드 검증:
//   - trust_score 필드 누락 → error (zero-value fabricated score 회피)
//   - rationale 누락 또는 whitespace-only → error (운영자 진단 정보 결핍 회피)
//   - trust_score 범위 [0.0, 1.0] 위반 → error (DB CHECK 와 일관)
func parseScoreOutput(output string) (*TrustScoreResult, error) {
	body := stripFences(output)
	if body == "" {
		return nil, errors.New("empty output")
	}
	var raw scoreResponse
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if raw.TrustScore == nil {
		return nil, errors.New("missing trust_score")
	}
	if strings.TrimSpace(raw.Rationale) == "" {
		return nil, errors.New("missing rationale")
	}
	if *raw.TrustScore < 0 || *raw.TrustScore > 1 {
		return nil, fmt.Errorf("trust_score %v out of range [0.0, 1.0]", *raw.TrustScore)
	}
	return &TrustScoreResult{
		TrustScore: *raw.TrustScore,
		Rationale:  raw.Rationale,
		Factors:    raw.Factors,
	}, nil
}

// isEmptyJSON 는 byte slice 가 비어있거나 빈 JSON ({}, [], null) 인지 검사합니다.
//
// whitespace 도 흡수 — `" {}\n"` 같은 변형도 empty 로 인식 (coderabbit-review PR #455).
func isEmptyJSON(b []byte) bool {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return true
	}
	switch s {
	case "{}", "[]", "null":
		return true
	}
	return false
}

func jsonOrEmptyObject(b []byte) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

func jsonOrEmptyArray(b []byte) string {
	if len(b) == 0 {
		return "[]"
	}
	return string(b)
}
