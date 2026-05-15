// 본 파일은 enrich 단계의 교차 검증기 (Verifier) 인터페이스 + 구현체를 정의합니다 (이슈 #448).
//
// Verifier 는 추출된 claims 와 후보 reference URL 들을 받아 claim 별 verdict 를 산출합니다.
// Claude WebFetch 도구를 적극 활용하도록 prompt 가 유도 — 후보 URL 외에도 추가 신규 페치
// 가능. Extractor 와 마찬가지로 실패 시 빈 verifications 로 fallback 하고 pipeline 진행.

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"issuetracker/pkg/llm/prompt"
)

// CandidateRef 는 DB 에서 추출한 동일 국가·시간 윈도우 후보 article 의 lightweight ref 입니다.
type CandidateRef struct {
	URL   string
	Title string
	Host  string
}

// VerifyInput 은 Verifier.Verify 의 입력 — source URL + 검증 대상 claims + DB 후보.
type VerifyInput struct {
	URL        string
	Host       string
	Title      string
	Claims     []Claim
	Candidates []CandidateRef
}

// Verifier 는 claims 의 외부 소스 대조 결과 (Verification 리스트) 를 산출합니다.
//
// 실패 시 worker 가 빈 verifications 로 fallback — forward 보장 (forward-first 정책).
type Verifier interface {
	Verify(ctx context.Context, in VerifyInput) ([]Verification, error)
}

// NoopVerifier 는 항상 빈 verifications 를 반환합니다 — claudegen 미configured 환경 fallback.
type NoopVerifier struct{}

// NewNoopVerifier 는 NoopVerifier 인스턴스를 반환합니다.
func NewNoopVerifier() *NoopVerifier { return &NoopVerifier{} }

// Verify 는 항상 빈 []Verification 을 반환합니다.
func (v *NoopVerifier) Verify(_ context.Context, _ VerifyInput) ([]Verification, error) {
	return []Verification{}, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ Verifier = (*NoopVerifier)(nil)

// verifierPromptName 은 cross-verify prompt asset 경로입니다.
const verifierPromptName = "enrich/claude/cross_verify.user"

// ClaudegenVerifier 는 claude.Pool 로 cross-verify session 을 실행합니다.
type ClaudegenVerifier struct {
	runner SessionRunner
	loader prompt.Loader
}

// NewClaudegenVerifier 는 claudegen-backed Verifier 를 생성합니다.
func NewClaudegenVerifier(runner SessionRunner, loader prompt.Loader) (*ClaudegenVerifier, error) {
	if runner == nil {
		return nil, errors.New("enrich/core: agent runner must not be nil")
	}
	if loader == nil {
		return nil, errors.New("enrich/core: prompt loader must not be nil")
	}
	return &ClaudegenVerifier{runner: runner, loader: loader}, nil
}

// Verify 는 claudegen 세션을 실행하고 stdout 을 []Verification 으로 파싱합니다.
//
// 입력 claims 가 비어있으면 LLM 호출 skip — 빈 verifications 반환.
func (v *ClaudegenVerifier) Verify(ctx context.Context, in VerifyInput) ([]Verification, error) {
	if len(in.Claims) == 0 {
		return []Verification{}, nil
	}

	tpl, err := v.loader.Load(verifierPromptName)
	if err != nil {
		return nil, fmt.Errorf("load verifier prompt %q: %w", verifierPromptName, err)
	}

	claimsJSON, err := marshalIndexedClaimsForPrompt(in.Claims)
	if err != nil {
		return nil, fmt.Errorf("marshal claims: %w", err)
	}
	candidatesJSON, err := marshalSliceForPrompt(in.Candidates)
	if err != nil {
		return nil, fmt.Errorf("marshal candidates: %w", err)
	}

	promptText := prompt.Render(tpl,
		"{{URL}}", in.URL,
		"{{HOST}}", in.Host,
		"{{TITLE}}", in.Title,
		"{{CLAIMS_JSON}}", claimsJSON,
		"{{CANDIDATES_JSON}}", candidatesJSON,
	)

	stdout, err := v.runner.RunSession(ctx, "enrich-verify", nil, promptText)
	if err != nil {
		return nil, fmt.Errorf("claudegen verify session: %w", err)
	}

	verifications, err := parseVerifyOutput(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse verify output: %w", err)
	}
	return verifications, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ Verifier = (*ClaudegenVerifier)(nil)

// verifyResponse 는 claudegen verify 응답의 wire format 입니다.
type verifyResponse struct {
	Verifications []Verification `json:"verifications"`
}

// parseVerifyOutput 는 claudegen stdout JSON 을 []Verification 으로 파싱합니다.
//
// 응답 schema:
//
//	{ "verifications": [ {claim_idx, verdict, sources, note}, ... ] }
//
// markdown code fence 도 stripFences 로 자동 제거 (Extractor 와 동일).
func parseVerifyOutput(output string) ([]Verification, error) {
	body := stripFences(output)
	if body == "" {
		return nil, errors.New("empty output")
	}
	var res verifyResponse
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if res.Verifications == nil {
		res.Verifications = []Verification{}
	}
	// verdict 정규화 — LLM 이 임의 string 을 반환할 수 있으므로 unknown 값은 "unverified" 로 폴백.
	for i := range res.Verifications {
		switch res.Verifications[i].Verdict {
		case "supported", "contradicted", "unverified":
			// OK
		default:
			res.Verifications[i].Verdict = "unverified"
		}
	}
	return res.Verifications, nil
}

// marshalSliceForPrompt 는 임의의 슬라이스를 prompt 용 JSON 문자열로 직렬화합니다 (이슈 #449, gemini-review PR #454).
//
// 입력 슬라이스가 비어있으면 "[]" 반환. Entities / Candidates / Claims(no-idx) 등 idx 가공이
// 필요 없는 케이스에서 공통으로 사용. idx 부착이 필요한 claims (verifier 입력) 는
// marshalIndexedClaimsForPrompt 별도 사용.
func marshalSliceForPrompt[T any](v []T) (string, error) {
	if len(v) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// marshalIndexedClaimsForPrompt 는 claims 를 verifier prompt 용으로 idx 부착 직렬화합니다.
//
// verifier prompt 의 출력 (verifications) 이 claim_idx 로 입력 claim 을 가리키므로 호출자
// (verifier) 에게 idx 명시 형태가 필수. contextualizer 처럼 idx 가 불필요한 경로는
// marshalSliceForPrompt(claims) 직접 사용 가능.
func marshalIndexedClaimsForPrompt(claims []Claim) (string, error) {
	type indexedClaim struct {
		Idx       int    `json:"idx"`
		Text      string `json:"text"`
		Subject   string `json:"subject,omitempty"`
		Predicate string `json:"predicate,omitempty"`
		Object    string `json:"object,omitempty"`
	}
	out := make([]indexedClaim, 0, len(claims))
	for i, c := range claims {
		out = append(out, indexedClaim{
			Idx: i, Text: c.Text, Subject: c.Subject, Predicate: c.Predicate, Object: c.Object,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
