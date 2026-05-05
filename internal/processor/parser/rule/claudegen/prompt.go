package claudegen

import (
	"fmt"

	"issuetracker/internal/storage"
)

// buildPrompt 는 Claude Code 에 전달할 프롬프트를 생성합니다.
// Claude Code 는 /workspace/page.html 을 파일 읽기 툴로 직접 접근합니다.
func buildPrompt(host string, targetType storage.TargetType) string {
	fieldsGuide := fieldsGuideFor(targetType)
	return fmt.Sprintf(`Read the HTML file at /workspace/page.html from the %s website.

Your task: extract CSS selectors for the fields below and return them as a **single JSON object only** — no explanation, no markdown, no code blocks.

Target page type: %s

Required fields:
%s

Rules:
1. Use only selectors that actually appear in the HTML — do not invent.
2. Prefer stable selectors (semantic tags, ARIA roles, id/class with meaningful names) over brittle ones (auto-generated hashes, nth-child indices).
3. "css" must be a valid goquery/CSS selector. "attribute" must be empty for text content, or the HTML attribute name (e.g. "href", "datetime", "content").
4. "multi": true when the selector matches multiple elements (lists, paragraphs).
5. Omit fields you cannot find a reliable selector for.

Return ONLY the JSON object.`, host, string(targetType), fieldsGuide)
}

func fieldsGuideFor(targetType storage.TargetType) string {
	switch targetType {
	case storage.TargetTypeList:
		return `{
  "item_container": {"css": "<selector for each list item root>"},
  "item_link":      {"css": "<selector inside item for the article link>", "attribute": "href"},
  "item_title":     {"css": "<selector inside item for the title text>"},
  "item_snippet":   {"css": "<selector inside item for the short summary>"}
}`
	default: // TargetTypePage / TargetTypeArticle
		return `{
  "title":        {"css": "<selector for page title>"},
  "main_content": {"css": "<selector for primary article body>", "multi": true},
  "summary":      {"css": "<selector for description/summary, often meta>", "attribute": "content"},
  "author":       {"css": "<selector for author name>"},
  "published_at": {"css": "<selector for publish datetime>", "attribute": "datetime"},
  "category":     {"css": "<selector for section/category>"},
  "tags":         {"css": "<selector for tag links>", "multi": true},
  "images":       {"css": "<selector for content images>", "attribute": "src", "multi": true}
}`
	}
}
