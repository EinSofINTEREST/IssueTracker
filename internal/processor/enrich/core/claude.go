package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"issuetracker/pkg/llm/prompt"
)

// SessionRunner 는 claude.Pool 의 RunEnrichSession 시그니처와 일치하는 추상.
//
// 본 패키지가 claudegen 패키지를 직접 import 하면 enrich → claudegen 의존이 추가됨.
// 인터페이스로 추상화하여 enrich 패키지가 claudegen 을 모르도록 — 테스트도 mock 으로 간단.
type SessionRunner interface {
	RunEnrichSession(
		ctx context.Context,
		sessionLabel string,
		files map[string][]byte,
		promptText string,
	) (string, error)
}

// promptName 은 enrich extraction 에 사용할 prompt asset 경로입니다.
const promptName = "claudegen/enricher_extract"

// ClaudegenExtractor 는 claude.Pool 을 통해 EnrichedFacts 를 추출합니다.
type ClaudegenExtractor struct {
	runner SessionRunner
	loader prompt.Loader
}

// NewClaudegenExtractor 는 claudegen-backed Extractor 를 생성합니다.
//
// runner 가 nil 이면 error — 호출자 (main.go) 가 claudegen pool 부재 시 NoopExtractor 사용.
// loader 가 nil 이면 error — prompt asset 로드 불가.
func NewClaudegenExtractor(runner SessionRunner, loader prompt.Loader) (*ClaudegenExtractor, error) {
	if runner == nil {
		return nil, errors.New("extractor: claudegen runner must not be nil")
	}
	if loader == nil {
		return nil, errors.New("extractor: prompt loader must not be nil")
	}
	return &ClaudegenExtractor{runner: runner, loader: loader}, nil
}

// Extract 는 claudegen 으로 session 을 실행하고 stdout 을 EnrichedFacts 로 파싱합니다.
//
// 실패 시 error 를 반환 — 호출자 (enrich worker) 는 빈 facts 로 fallback 결정.
func (e *ClaudegenExtractor) Extract(ctx context.Context, in Input) (*EnrichedFacts, error) {
	tpl, err := e.loader.Load(promptName)
	if err != nil {
		return nil, fmt.Errorf("load enrich prompt %q: %w", promptName, err)
	}

	// 본 sub-issue 에서는 session_path 가 컨테이너 내부 경로로 직접 노출 — claudegen Worker 의
	// 세션 디렉토리는 /workspace/<sessionID> 에 마운트. 본 path 를 caller 가 알 필요 없도록
	// claudegen 내부에서 처리하고, prompt 안에서는 sessionPath placeholder 를 사용.
	//
	// 다만 RunEnrichSession 은 sessionPath 를 반환하지 않으므로, prompt template 의
	// {{SESSION_PATH}} 가 컨테이너 표준 경로로 치환되도록 caller 가 placeholder 를 미리 알아야 함.
	// 현재 단계: prompt 안에서 page.html 을 직접 절대 경로로 참조하는 대신 "Read the HTML file
	// in your session workspace" 식으로 묶어 처리 가능. 본 sub-issue 는 ExtractEnriched 와
	// 동일하게 /workspace/<sessionID>/page.html 컨테이너 path 를 사용하나, RunEnrichSession
	// 이 sessionID 를 외부로 노출하지 않으므로 prompt 에는 상대 경로만 기록한다.
	//
	// 단순화: prompt 에서 {{SESSION_PATH}} 를 그대로 두지 않고, page.html 만 사용한다고 명시 →
	// claude 가 cwd 의 page.html 을 읽도록 한다. RunEnrichSession 은 컨테이너 cwd 를
	// 세션 디렉토리로 두므로 동일 효과.
	//
	// 향후 (#448 의 cross-verify) 가 sessionPath 가 필요하면 RunEnrichSession 시그니처 확장.
	promptText := prompt.Render(tpl,
		"{{SESSION_PATH}}", ".", // 컨테이너 cwd 가 세션 디렉토리이므로 상대경로
		"{{HOST}}", in.Host,
		"{{URL}}", in.URL,
		"{{TITLE}}", in.Title,
	)

	files := map[string][]byte{
		"page.html": []byte(in.HTML),
	}

	stdout, err := e.runner.RunEnrichSession(ctx, "enrich-extract", files, promptText)
	if err != nil {
		return nil, fmt.Errorf("claudegen enrich session: %w", err)
	}

	facts, err := parseEnrichOutput(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse enrich output: %w", err)
	}
	return facts, nil
}

// parseEnrichOutput 는 claudegen stdout JSON 을 EnrichedFacts 로 파싱합니다.
//
// 응답이 markdown code fence 로 감싸진 경우도 견고하게 처리 — claude 가 prompt 지시에도
// 가끔 ```json ... ``` 로 감싸 반환.
func parseEnrichOutput(output string) (*EnrichedFacts, error) {
	body := stripFences(strings.TrimSpace(output))
	if body == "" {
		return nil, errors.New("empty output")
	}
	var raw EnrichedFacts
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	// nil slice → empty slice 정규화 (downstream 코드의 nil/empty 분기 단순화).
	if raw.Entities == nil {
		raw.Entities = []Entity{}
	}
	if raw.Claims == nil {
		raw.Claims = []Claim{}
	}
	if raw.Facts == nil {
		raw.Facts = []Fact{}
	}
	if raw.Topics == nil {
		raw.Topics = []string{}
	}
	if raw.Sentiment == "" {
		raw.Sentiment = SentimentNeutral
	}
	return &raw, nil
}

// stripFences 는 응답에서 markdown code fence (```json ... ```) 를 견고하게 제거합니다.
//
// 처리 시나리오 (gemini-review PR #452 반영):
//   - prompt 위반으로 fence 외부에 대화 텍스트 (예: "Here is the JSON:") 가 있어도
//     첫 fence 의 시작점을 찾아 그 이전 텍스트는 버림.
//   - 언어 식별자 (json/javascript 등) 가 newline 없이 붙어 있어도 ('json{"k":1}')
//     JSON 시작 토큰 ({ 또는 [) 까지 skip.
//   - 닫는 fence 뒤 텍스트가 있어도 last `\x60\x60\x60` 위치로 자름.
//   - fence 가 전혀 없는 응답은 그대로 trim 만 적용.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	startIdx := strings.Index(s, "```")
	if startIdx == -1 {
		return s
	}
	// 첫 fence 진입 — 그 이전 텍스트는 무시.
	s = s[startIdx+3:]

	// 언어 식별자 처리: newline 까지 skip. newline 없으면 JSON 시작 토큰 위치로 점프.
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[idx+1:]
	} else if start := strings.IndexAny(s, "{["); start != -1 {
		s = s[start:]
	}

	// 닫는 fence 위치 (최근 발견) — 뒤쪽 텍스트 제거.
	if idx := strings.LastIndex(s, "```"); idx != -1 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ Extractor = (*ClaudegenExtractor)(nil)
