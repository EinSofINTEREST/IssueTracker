package llmgen

import (
	"fmt"
	"strings"

	"issuetracker/internal/storage"
)

// promptMaxHTMLBytes 는 프롬프트에 첨부할 HTML 의 최대 바이트 수입니다.
//
// LLM context window 와 비용을 절약하기 위해 큰 페이지는 앞부분만 첨부합니다.
// HTML 의 핵심 구조 (<head>, <article>, <main>, top-level container) 는 보통 앞쪽에
// 위치하므로 selector 추출에는 충분. 너무 짧으면 동적 로드되는 본문 부분을 놓치므로
// 32KB 가 합리적 baseline.
const promptMaxHTMLBytes = 32 * 1024

// BuildPrompt 는 host + target_type + 샘플 HTML 로 LLM 시스템/사용자 프롬프트를 생성합니다.
//
// LLM 응답 형식 강제 — 반드시 단일 JSON 객체로 응답하도록 지시.
// JSON 구조는 storage.SelectorMap 의 JSON tag 와 1:1 매핑되어 그대로 unmarshal 가능.
//
// 반환값: (system, user) — Provider 호출 시 RoleSystem + RoleUser 메시지로 분리 전달.
func BuildPrompt(host string, targetType storage.TargetType, html string) (system, user string) {
	system = systemPrompt
	user = buildUserPrompt(host, targetType, truncateHTML(html))
	return system, user
}

const systemPrompt = `You are an expert web scraper. Given an HTML sample of a webpage, extract CSS selectors for the most important fields and return them as a single JSON object.

Strict rules:
1. Respond ONLY with a single JSON object — no prose, no markdown fences, no comments.
2. Use only selectors that actually appear in the provided HTML — do not invent.
3. Prefer stable selectors (semantic tags, ARIA roles, id/class with meaningful names) over brittle ones (auto-generated hashes, nth-child indices).
4. If a field cannot be confidently identified, omit it (do not guess).
5. The "css" value must be a valid goquery selector. The "attribute" value must be empty for text content, or the HTML attribute name (e.g. "href", "src", "datetime", "content").
6. Use "multi": true only when the field contains multiple distinct values (tags, image lists, paragraph blocks).`

// buildUserPrompt 는 target_type 별로 요청 필드를 다르게 안내합니다.
func buildUserPrompt(host string, targetType storage.TargetType, html string) string {
	var fieldsGuide string
	switch targetType {
	case storage.TargetTypeList:
		fieldsGuide = `Required JSON shape (omit fields you cannot identify):
{
  "item_container": {"css": "<selector for each list item root>"},
  "item_link":      {"css": "<selector inside item for the article link>", "attribute": "href"},
  "item_title":     {"css": "<selector inside item for the title text>"},
  "item_snippet":   {"css": "<selector inside item for the short summary>"}
}

Goal: extract a list of article links from a hub/category page.`
	default: // TargetTypePage and unknown — default to single-page extraction.
		fieldsGuide = `Required JSON shape (omit fields you cannot identify):
{
  "title":        {"css": "<selector for page title>"},
  "main_content": {"css": "<selector for primary article body>", "multi": true},
  "summary":      {"css": "<selector for description/summary, often meta>", "attribute": "content"},
  "author":       {"css": "<selector for author name>"},
  "published_at": {"css": "<selector for publish datetime>", "attribute": "datetime"},
  "category":     {"css": "<selector for section/category>"},
  "tags":         {"css": "<selector for tag links>", "multi": true},
  "images":       {"css": "<selector for content images>", "attribute": "src", "multi": true}
}

Goal: extract a single content page (article / blog post / document).`
	}

	return fmt.Sprintf(`Host: %s
Target type: %s

%s

HTML sample (truncated to %d bytes if longer):
---
%s
---

Return ONLY the JSON object.`, host, string(targetType), fieldsGuide, promptMaxHTMLBytes, html)
}

// truncateHTML 은 HTML 을 promptMaxHTMLBytes 까지 잘라냅니다 (UTF-8 경계 안전).
func truncateHTML(html string) string {
	if len(html) <= promptMaxHTMLBytes {
		return html
	}
	cut := html[:promptMaxHTMLBytes]
	// 마지막 valid UTF-8 경계까지 줄임 — string 슬라이스가 multi-byte 중간에서 잘릴 수 있음.
	for i := len(cut) - 1; i >= 0 && i > len(cut)-4; i-- {
		if cut[i] < 0x80 || cut[i] >= 0xC0 {
			return cut[:i]
		}
	}
	return cut
}

// extractJSON 은 LLM 응답에서 첫 번째 JSON 객체 substring 을 추출합니다.
//
// LLM 이 markdown 코드 펜스 (```json ... ```) 나 설명 prose 를 함께 반환하는 경우를 대비해
// 첫 번째 '{' 부터 균형 맞는 마지막 '}' 까지 파싱. 따옴표 안의 brace 는 무시.
//
// 매칭 실패 시 빈 문자열 반환 — 호출자가 ErrInvalidResponse 처리.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}

	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' && inString {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
