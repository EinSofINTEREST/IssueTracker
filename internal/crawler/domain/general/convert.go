package general

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/parser"
	"issuetracker/internal/storage"
)

// ConvertPage 는 parser.Page + RawContent 를 core.Content 로 변환합니다.
// URL 우선순위: page.URL → raw.URL.
//
// ConvertPage converts a parser.Page and RawContent into a core.Content.
// 기존 ConvertArticle 의 도메인 일반화 — Article DTO 대신 parser.Page 를 직접 입력으로 받음.
func ConvertPage(page *parser.Page, raw *core.RawContent) *core.Content {
	url := page.URL
	if url == "" {
		url = raw.URL
	}

	return &core.Content{
		ID:           ContentID(url),
		SourceID:     raw.SourceInfo.Name,
		SourceType:   raw.SourceInfo.Type,
		Country:      raw.SourceInfo.Country,
		Language:     raw.SourceInfo.Language,
		Title:        page.Title,
		Body:         page.MainContent,
		Summary:      page.Summary,
		Author:       page.Author,
		PublishedAt:  page.PublishedAt,
		Category:     page.Category,
		Tags:         page.Tags,
		URL:          url,
		CanonicalURL: url,
		ImageURLs:    page.Images,
		WordCount:    len(strings.Fields(page.MainContent)),
		ContentHash:  ContentHash(page.MainContent),
		CreatedAt:    time.Now(),
	}
}

// PageToRecord 는 parser.Page + RawContent 를 storage.NewsArticleRecord 로 변환합니다.
// 모든 사이트가 news_articles 테이블에 기록 — 본 함수 호출은 옵셔널 (chain handler 에서 repo nil 검사).
//
// URL 우선순위는 ConvertPage 와 동일 — page.URL → raw.URL. 파서가 canonical URL 을
// 채운 경우 두 출력 (Content + Record) 이 같은 URL 을 보유해 dedup 정합성 유지
// (Coderabbit / Gemini 피드백 — 일관성 강화).
func PageToRecord(page *parser.Page, raw *core.RawContent) *storage.NewsArticleRecord {
	url := page.URL
	if url == "" {
		url = raw.URL
	}
	record := &storage.NewsArticleRecord{
		SourceName: raw.SourceInfo.Name,
		SourceType: string(raw.SourceInfo.Type),
		Country:    raw.SourceInfo.Country,
		Language:   raw.SourceInfo.Language,
		URL:        url,
		Title:      page.Title,
		Body:       page.MainContent,
		Summary:    page.Summary,
		Author:     page.Author,
		Category:   page.Category,
		Tags:       page.Tags,
		ImageURLs:  page.Images,
		FetchedAt:  raw.FetchedAt,
	}
	if !page.PublishedAt.IsZero() {
		t := page.PublishedAt
		record.PublishedAt = &t
	}
	return record
}

// ContentID 는 URL 의 SHA-256 앞 16바이트 hex.
func ContentID(url string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:16])
}

// ContentHash 는 본문 SHA-256 hex.
func ContentHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}
