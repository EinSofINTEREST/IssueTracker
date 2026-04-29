// Package parser 은 모든 웹페이지 (뉴스 / 블로그 / 일반 문서) 의 핵심 내용을
// 추출하기 위한 도메인 중립 인터페이스와 모델을 제공합니다 (이슈 #100).
//
// Package parser defines domain-agnostic interfaces and models for extracting
// the main content of any web page. 사이트별 hardcode 파서를 대체하여, DB 기반 rule
// (storage.ParsingRuleRecord) 만 다른 단일 engine 이 모든 웹페이지를 처리합니다.
//
// 두 핵심 인터페이스:
//   - ContentParser  : 단일 웹페이지 → Page (핵심 본문 + 메타데이터)
//   - LinkListParser : 카테고리/목록/링크-허브 페이지 → []LinkItem
//
// 뉴스 도메인의 NewsArticleParser/NewsListParser 는 본 인터페이스의 도메인 어댑터로
// 표현 가능합니다 (Page → NewsArticle 변환은 호출자 책임).
package parser

import (
	"time"

	"issuetracker/internal/crawler/core"
)

// Page 는 임의 웹페이지에서 추출한 핵심 내용입니다.
//
// Page represents the extracted main content of a web page (article, blog post,
// product page, etc). 모든 필드는 optional 이며 (URL/Title/MainContent 외에는 빈 값
// 허용), 사이트의 rule selectors 가 비어있으면 그 필드는 zero 값으로 남습니다.
//
// 뉴스 도메인 사용 시 호출자가 NewsArticle 로 변환:
//
//	news.NewsArticle{
//	    Title:       page.Title,
//	    Body:        page.MainContent,
//	    PublishedAt: page.PublishedAt,
//	    ...
//	}
type Page struct {
	URL         string
	Title       string
	MainContent string    // 페이지 핵심 본문 (article body, blog post, product description 등)
	Summary     string    // optional — meta description 또는 별도 요약 영역
	Author      string    // optional — 게시자/저자 (기사 / 블로그 등)
	PublishedAt time.Time // optional — zero 면 미추출
	Language    string    // optional — html lang 또는 메타 (ISO 639-1)
	Category    string    // optional — 카테고리/섹션 (블로그 카테고리, 제품 카테고리 등)
	Tags        []string
	Images      []string          // optional — page 내 핵심 이미지 URL
	Metadata    map[string]string // 확장 — canonical_url / og:* / twitter:* 등 임의 메타
}

// LinkItem 은 목록/링크-허브 페이지에서 추출한 단일 링크입니다.
//
// LinkItem represents a single link extracted from a list/category/hub page.
// URL 은 항상 절대 URL 로 정규화되어야 합니다 (LinkListParser 구현체가 base URL 기준 변환).
type LinkItem struct {
	URL     string
	Title   string // anchor text 또는 추출한 제목
	Snippet string // optional — 짧은 요약/설명 (있을 때)
}

// ContentParser 는 웹페이지의 RawContent 를 Page 로 파싱하는 인터페이스입니다.
//
// ContentParser parses a single web page's RawContent into a Page.
// 구현체는 goroutine-safe 해야 합니다.
type ContentParser interface {
	ParsePage(raw *core.RawContent) (*Page, error)
}

// LinkListParser 는 목록/링크-허브 페이지에서 LinkItem 들을 추출하는 인터페이스입니다.
//
// LinkListParser extracts LinkItem entries from a list/category page.
// 구현체는 goroutine-safe 해야 합니다.
type LinkListParser interface {
	ParseLinks(raw *core.RawContent) ([]LinkItem, error)
}
