package general

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/parser"
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
