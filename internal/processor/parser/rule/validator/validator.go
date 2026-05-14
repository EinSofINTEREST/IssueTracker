// Package validator 는 LLM 으로 생성된 CSS 셀렉터의 의미 검증을 담당합니다.
//
// 역할: DOM 매칭 검증 (validateSelectors) 이 통과한 뒤, 실제 추출 내용이 뉴스 기사의
// 제목·본문·날짜로서 의미상 유효한지 LLM 으로 확인합니다.
//
// 흐름:
//  1. CSS 셀렉터로 HTML 에서 텍스트 추출 (goquery)
//  2. 추출 내용 + 검증 기준을 LLM 에 전달
//  3. LLM 응답 ({"valid":true/false,"reason":"..."}) 파싱
//  4. false 이면 selectorValidationError 로 반환 → rule INSERT 차단
package validator

import (
	"context"
	"issuetracker/internal/storage/model"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Result 는 의미 검증 결과입니다.
type Result struct {
	Valid  bool
	Reason string
}

// Validator 는 셀렉터 의미 검증 인터페이스입니다.
//
// goroutine-safe 필수 — Generator 의 background goroutine 에서 호출됩니다.
type Validator interface {
	// Validate 는 HTML 과 SelectorMap 을 받아 추출 내용의 의미 적합성을 검증합니다.
	// 검증 API 오류는 error 로 반환 (호출자가 best-effort 처리) — semantic reject 는 Result.Valid=false.
	Validate(ctx context.Context, html string, selectors model.SelectorMap, targetType model.TargetType) (Result, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// 내부 헬퍼 — HTML 에서 셀렉터별 텍스트 추출
// ─────────────────────────────────────────────────────────────────────────────

// extractedContent 는 셀렉터로 추출한 텍스트 스냅샷입니다.
type extractedContent struct {
	Title         string
	Body          string
	PublishedAt   string
	ItemContainer string
	ItemLinks     []string
}

// extractContent 는 SelectorMap 의 각 셀렉터로 HTML 에서 텍스트를 추출합니다.
func extractContent(html string, sm model.SelectorMap) (extractedContent, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return extractedContent{}, err
	}

	var ec extractedContent

	if sm.Title != nil && sm.Title.CSS != "" {
		ec.Title = strings.TrimSpace(doc.Find(sm.Title.CSS).First().Text())
	}
	if sm.MainContent != nil && sm.MainContent.CSS != "" {
		// Multi=false(기본) 이면 첫 번째 매칭만 사용 — 실제 parser 동작과 일치해야 의미 검증이 유효.
		var body string
		if sm.MainContent.Multi {
			var parts []string
			doc.Find(sm.MainContent.CSS).Each(func(_ int, s *goquery.Selection) {
				if t := strings.TrimSpace(s.Text()); t != "" {
					parts = append(parts, t)
				}
			})
			body = strings.Join(parts, " ")
		} else {
			body = strings.TrimSpace(doc.Find(sm.MainContent.CSS).First().Text())
		}
		// 최대 500자 — 프롬프트 크기 제한
		if len([]rune(body)) > 500 {
			runes := []rune(body)
			body = string(runes[:500]) + "…"
		}
		ec.Body = body
	}
	if sm.PublishedAt != nil && sm.PublishedAt.CSS != "" {
		sel := doc.Find(sm.PublishedAt.CSS).First()
		if attr := sm.PublishedAt.Attribute; attr != "" {
			ec.PublishedAt, _ = sel.Attr(attr)
		} else {
			ec.PublishedAt = strings.TrimSpace(sel.Text())
		}
	}
	if sm.ItemContainer != nil && sm.ItemContainer.CSS != "" {
		container := doc.Find(sm.ItemContainer.CSS).First()
		ec.ItemContainer = strings.TrimSpace(container.Text())
		if len([]rune(ec.ItemContainer)) > 200 {
			runes := []rune(ec.ItemContainer)
			ec.ItemContainer = string(runes[:200]) + "…"
		}
		// ItemLink 는 ItemContainer 내부에서 수집 — 전체 문서에서 수집하면 네비게이션/푸터 링크가
		// 섞여 잘못된 list rule 이 의미 검증을 통과할 수 있음.
		if sm.ItemLink != nil && sm.ItemLink.CSS != "" {
			container.Find(sm.ItemLink.CSS).Each(func(i int, s *goquery.Selection) {
				if i >= 3 {
					return
				}
				if attr := sm.ItemLink.Attribute; attr != "" {
					if v, ok := s.Attr(attr); ok {
						ec.ItemLinks = append(ec.ItemLinks, v)
					}
				} else {
					ec.ItemLinks = append(ec.ItemLinks, strings.TrimSpace(s.Text()))
				}
			})
		}
	} else if sm.ItemLink != nil && sm.ItemLink.CSS != "" {
		// ItemContainer 없이 ItemLink 만 있는 경우 — 문서 전체에서 수집
		doc.Find(sm.ItemLink.CSS).Each(func(i int, s *goquery.Selection) {
			if i >= 3 {
				return
			}
			if attr := sm.ItemLink.Attribute; attr != "" {
				if v, ok := s.Attr(attr); ok {
					ec.ItemLinks = append(ec.ItemLinks, v)
				}
			} else {
				ec.ItemLinks = append(ec.ItemLinks, strings.TrimSpace(s.Text()))
			}
		})
	}
	return ec, nil
}
