// 본 파일은 rule.Parser 의 selector 추출 helper 함수 모음입니다 (이슈 #463 분리).
//
// parser.go 가 engine 진입점 (ParsePage / ParseLinks + ParseLink 모드 분기) 만 담당하도록
// 추출 관련 helper 를 본 파일로 이전 — 책임 분리.

package rule

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage/model"
)

// hasRequiredSelector 는 selector 가 lookup 가능한지 검사합니다 (Coderabbit 피드백).
// nil 이거나 CSS 가 trim 후 빈 문자열이면 false — DB 의 zero-value row 도 명확히 reject.
func hasRequiredSelector(fs *model.FieldSelector) bool {
	return fs != nil && strings.TrimSpace(fs.CSS) != ""
}

// validateRaw 는 ParsePage / ParseLinks 의 공통 raw 검증입니다.
// raw 가 nil 이거나 HTML 이 비어있으면 (whitespace-only 도 빈 것으로 간주) raw.URL 진단 정보 포함 Error 반환.
// (Coderabbit 피드백: "   \n" 같은 whitespace-only 가 통과해 stale-rule 오인 회피)
func validateRaw(raw *core.RawContent) error {
	if raw != nil && strings.TrimSpace(raw.HTML) != "" {
		return nil
	}
	var u string
	if raw != nil {
		u = raw.URL
	}
	return &Error{Code: ErrParseFailure, Message: "raw content empty", URL: u}
}

// extractField 는 단일 필드를 추출합니다 (selector 가 nil 이면 빈 문자열).
//
// Multi=false (기본): 첫 매칭 element 의 값 반환.
// Multi=true: 모든 매칭 element 의 값을 줄바꿈으로 합쳐 반환 (MainContent 다중 단락 등).
func extractField(doc *goquery.Document, fs *model.FieldSelector) string {
	if fs == nil || fs.CSS == "" {
		return ""
	}
	return extractFromSelection(doc.Selection, fs)
}

// extractFieldFromSelection 은 sub-selection 안에서 필드를 추출합니다 (list item 내부 lookup).
func extractFieldFromSelection(s *goquery.Selection, fs *model.FieldSelector) string {
	if fs == nil || fs.CSS == "" {
		return ""
	}
	return extractFromSelection(s, fs)
}

// extractFromSelection 은 selection 범위에서 selector 적용 결과를 반환합니다.
func extractFromSelection(scope *goquery.Selection, fs *model.FieldSelector) string {
	matched := scope.Find(fs.CSS)
	if matched.Length() == 0 {
		return ""
	}
	if !fs.Multi {
		return strings.TrimSpace(extractValue(matched.First(), fs.Attribute))
	}
	var parts []string
	matched.Each(func(_ int, sel *goquery.Selection) {
		v := strings.TrimSpace(extractValue(sel, fs.Attribute))
		if v != "" {
			parts = append(parts, v)
		}
	})
	return strings.Join(parts, "\n")
}

// extractFieldMulti 는 multi 결과를 string 슬라이스로 반환합니다 (Tags / Images 용).
//
// extractField 의 multi 모드는 줄바꿈으로 합치지만, 본 함수는 각 element 를 별도 항목으로 보존.
func extractFieldMulti(doc *goquery.Document, fs *model.FieldSelector) []string {
	if fs == nil || fs.CSS == "" {
		return nil
	}
	matched := doc.Find(fs.CSS)
	if matched.Length() == 0 {
		return nil
	}
	out := make([]string, 0, matched.Length())
	matched.Each(func(_ int, sel *goquery.Selection) {
		v := strings.TrimSpace(extractValue(sel, fs.Attribute))
		if v != "" {
			out = append(out, v)
		}
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractValue 는 element 의 text (Attribute=="") 또는 attribute 값을 반환합니다.
func extractValue(sel *goquery.Selection, attribute string) string {
	if attribute == "" {
		return sel.Text()
	}
	v, _ := sel.Attr(attribute)
	return v
}

// extractDate 는 PublishedAt 필드를 추출하고 dateLayouts 를 순회 시도합니다.
// 추출 실패 시 zero time 반환 — 호출자 (validator 등) 가 zero 검사로 분기.
func (p *Parser) extractDate(doc *goquery.Document, fs *model.FieldSelector) time.Time {
	raw := extractField(doc, fs)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range p.dateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// absoluteURL 은 link 를 base 기준 절대 URL 로 변환합니다.
func absoluteURL(base *url.URL, link string) (string, error) {
	ref, err := url.Parse(link)
	if err != nil {
		return "", fmt.Errorf("parse link: %w", err)
	}
	return base.ResolveReference(ref).String(), nil
}

// defaultDateLayouts 는 PublishedAt 추출 시 시도할 layout 목록입니다.
// 사이트별 차이 (RFC3339 / Korean / ISO 8601 etc) 를 일반화. 운영 중 새 형식 발견 시 확장.
func defaultDateLayouts() []string {
	return []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006.01.02 15:04",
		"2006.01.02. 15:04",
		"2006.01.02",
		"2006/01/02 15:04:05",
	}
}
