package pathinfer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// LLMClient 는 InferLLM 이 사용하는 최소 LLM 호출 인터페이스입니다 (이슈 #173 단계 3).
//
// pathinfer 가 pkg/llm 을 직접 import 하지 않도록 작은 abstraction —
// 호출자 (단계 4 의 hybrid 흐름) 가 pkg/llm.Provider 위에 adapter 를 만들어 주입합니다.
//
// 구현체는 goroutine-safe 해야 합니다.
type LLMClient interface {
	// Generate 는 system + user 프롬프트로 LLM 호출 후 응답 텍스트를 반환합니다.
	// 호출자는 timeout / cancel 을 ctx 로 제어.
	Generate(ctx context.Context, system, user string) (string, error)
}

// LLMSamples 는 InferLLM 의 입력 샘플입니다.
//
// Articles : positive — 결과 regex 가 매칭해야 할 URL path 슬라이스.
// NonArticles : negative — 결과 regex 가 매칭하면 안 될 URL path (선택, 빈 슬라이스 허용).
//
// path 는 정규화된 URL 의 path 부분 (예: "/article/12345"). scheme/host 제거 권장.
type LLMSamples struct {
	Articles    []string
	NonArticles []string
}

// llmSystemPrompt 는 모든 InferLLM 호출에 공통으로 사용되는 system 프롬프트입니다.
//
// 응답 형식 강제 — markdown 펜스 / prose 없이 단일 RE2 패턴 라인만 반환하도록 지시.
// 본 PR scope: pathinfer 패키지 안 inline 상수. 이슈 #171 (프롬프트 외부 파일) 머지 후 그곳으로 이전 가능.
const llmSystemPrompt = `You are an expert at writing URL path regular expressions.

Strict rules:
1. Respond with ONLY a single RE2-compatible regex pattern on the first line — no prose, no markdown fences, no explanation.
2. The regex must match ALL provided positive (article) URL paths.
3. The regex must NOT match any of the provided negative (non-article) URL paths.
4. Avoid trivially broad patterns (e.g. ".*", "^/.*$", ".+") — produce a pattern that is as specific as possible while satisfying rules 2 and 3.
5. Anchor the pattern with ^ and $ if appropriate.
6. Use only standard RE2 syntax (no lookaround, no backreferences).`

// llmUserPromptTemplate 은 LLMSamples 를 채워 user 프롬프트를 생성합니다.
const llmUserPromptTemplate = `Given these URL paths from the same site, generate a single RE2 regex that matches the article URLs but not the non-article URLs.

Article URLs (positive — must match):
%s

Non-article URLs (negative — must NOT match):
%s

Respond with ONLY the regex pattern.`

// InferLLM 은 LLMClient 를 사용해 path_pattern regex 를 추론합니다 (이슈 #173 단계 3).
//
// 흐름:
//  1. samples.Articles 가 cfg.minSamples 미만이면 ("", false, nil) — pathinfer.InferHeuristic 과 동일 정책
//  2. LLM 호출 (system + user 프롬프트)
//  3. 응답에서 첫 번째 RE2 패턴 라인 추출 (markdown 펜스 무시)
//  4. 검증 (validateLLMResult): 컴파일 / positive / negative / trivially-broad
//  5. 검증 통과 시 (regex, true, nil), 실패 시 ("", false, nil) — 호출자가 catch-all 또는 다른 fallback 으로 분기
//
// LLM 호출 자체의 에러 (network / API) 는 (regex="", ok=false, err=원본) 로 반환 — 호출자가 retry / 알림 결정.
//
// opts 로 default override 가능 (WithMinSamples 등 — InferHeuristic 과 동일).
func InferLLM(ctx context.Context, samples LLMSamples, client LLMClient, opts ...Option) (string, bool, error) {
	cfg := config{minSamples: DefaultMinSamples}
	for _, o := range opts {
		o(&cfg)
	}

	if len(samples.Articles) < cfg.minSamples {
		return "", false, nil
	}
	if client == nil {
		return "", false, errors.New("InferLLM: nil LLMClient")
	}

	user := buildUserPrompt(samples)
	resp, err := client.Generate(ctx, llmSystemPrompt, user)
	if err != nil {
		return "", false, fmt.Errorf("llm generate: %w", err)
	}

	pattern := extractPattern(resp)
	if pattern == "" {
		return "", false, nil
	}

	if !validateLLMResult(pattern, samples) {
		return "", false, nil
	}
	return pattern, true, nil
}

// buildUserPrompt 는 samples 로 user 프롬프트 본문을 생성합니다.
//
// Articles / NonArticles 가 비어있으면 "(none)" 으로 표시 — LLM 이 빈 슬라이스를 "negative 없음"
// 으로 정확히 해석하도록.
func buildUserPrompt(samples LLMSamples) string {
	pos := joinOrNone(samples.Articles)
	neg := joinOrNone(samples.NonArticles)
	return fmt.Sprintf(llmUserPromptTemplate, pos, neg)
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, s := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(s)
	}
	return b.String()
}

// extractPattern 은 LLM 응답에서 첫 번째 RE2 패턴 라인을 추출합니다 (이슈 #173 단계 3).
//
// 처리 우선순위:
//  1. markdown 코드 펜스 (```...```) 가 응답 어느 위치에든 있으면 → 펜스 내부 첫 비어있지 않은 라인 우선 사용
//     (LLM 이 prose 먼저 출력 후 펜스로 regex 를 감싸는 케이스 cover — PR #187 CodeRabbit 피드백)
//  2. 펜스 없으면 응답 첫 비어있지 않은 라인 사용
//  3. 라인 trim (whitespace 제거)
//  4. 빈 결과 → ""
func extractPattern(resp string) string {
	resp = strings.TrimSpace(resp)
	if resp == "" {
		return ""
	}

	// fenced block 우선 — 응답 어느 위치에 있든 fence 안의 첫 비어있지 않은 라인 사용.
	if strings.Contains(resp, "```") {
		inFence := false
		for _, raw := range strings.Split(resp, "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, "```") {
				// single-line fence — ```pattern``` 형식 — 안의 패턴을 즉시 추출
				// (PR #187 gemini 피드백, ``` 가 한 라인에 양쪽으로 있는 경우).
				if strings.HasSuffix(line, "```") && len(line) > 6 {
					if inner := strings.TrimSpace(line[3 : len(line)-3]); inner != "" {
						return inner
					}
				}
				inFence = !inFence
				continue
			}
			if inFence && line != "" {
				return line
			}
		}
	}

	// fence 없거나 fence 안에 비어있지 않은 라인이 없으면 첫 비어있지 않은 라인 fallback.
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// validateLLMResult 는 LLM 응답 regex 가 모든 검증 단계를 통과하는지 확인합니다 (이슈 #173 단계 3).
//
// 검증 단계:
//  1. RE2 컴파일 가능
//  2. positive (samples.Articles) 100% 매칭
//  3. negative (samples.NonArticles) 0% 매칭
//  4. trivially broad 거부 (isTriviallyBroad)
//
// 모든 단계 통과 시 true, 1단계라도 실패 시 false.
func validateLLMResult(pattern string, samples LLMSamples) bool {
	if isTriviallyBroad(pattern) {
		return false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	// positive: 모든 article path 가 매칭되어야 함 (정규화된 형태로 매칭 — InferHeuristic 정책 일관)
	for _, p := range samples.Articles {
		if !re.MatchString("/" + strings.Trim(p, "/")) {
			return false
		}
	}
	// negative: 어떤 non-article 도 매칭되면 안 됨
	for _, p := range samples.NonArticles {
		if re.MatchString("/" + strings.Trim(p, "/")) {
			return false
		}
	}
	return true
}

// triviallyBroadPatterns 는 \"의미 있는 path 차별화 없음\" 으로 거부할 패턴 집합입니다.
//
// 호출자가 hand-tuned regex 를 운영 환경에서 잘못 입력 (예: \".*\") 한 케이스도 본 검증으로
// 차단됨 — LLM hallucination 외에도 운영 안전망 역할.
var triviallyBroadPatterns = map[string]struct{}{
	"":        {},
	".*":      {},
	".+":      {},
	"/.*":     {},
	"/.+":     {},
	"^.*$":    {},
	"^.+$":    {},
	"^/$":     {},
	"^/.*$":   {},
	"^/.+$":   {},
	"^/(.*)$": {},
	"^/(.+)$": {},
}

// isTriviallyBroad 는 pattern 이 \"의미 있는 path 차별화 없음\" 인지 확인합니다.
// 정규화 (trim whitespace) 후 set lookup.
func isTriviallyBroad(pattern string) bool {
	p := strings.TrimSpace(pattern)
	_, ok := triviallyBroadPatterns[p]
	return ok
}
