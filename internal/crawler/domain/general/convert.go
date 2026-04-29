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
func PageToRecord(page *parser.Page, raw *core.RawContent) *storage.NewsArticleRecord {
	record := &storage.NewsArticleRecord{
		SourceName: raw.SourceInfo.Name,
		SourceType: string(raw.SourceInfo.Type),
		Country:    raw.SourceInfo.Country,
		Language:   raw.SourceInfo.Language,
		URL:        raw.URL,
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

// ConvertRSSPages 는 RSS RawContent.Metadata["rss_pages"] 를 []*core.Content 로 변환합니다.
// metadata key 는 buildRSSRawContent 에서 채우며, page 단위 URL 누락 시 해당 항목 skip.
func ConvertRSSPages(raw *core.RawContent) []*core.Content {
	items, ok := raw.Metadata["rss_pages"].([]map[string]interface{})
	if !ok {
		return nil
	}
	contents := make([]*core.Content, 0, len(items))
	for _, item := range items {
		c := rssItemToContent(item, raw)
		if c == nil {
			continue
		}
		contents = append(contents, c)
	}
	return contents
}

func rssItemToContent(item map[string]interface{}, raw *core.RawContent) *core.Content {
	url, _ := item["url"].(string)
	if url == "" {
		return nil
	}
	title, _ := item["title"].(string)
	body, _ := item["main_content"].(string)
	author, _ := item["author"].(string)
	summary, _ := item["summary"].(string)

	var publishedAt time.Time
	if s, ok := item["published_at"].(string); ok && s != "" {
		publishedAt, _ = time.Parse(time.RFC3339, s)
	}

	return &core.Content{
		ID:           ContentID(url),
		SourceID:     raw.SourceInfo.Name,
		SourceType:   raw.SourceInfo.Type,
		Country:      raw.SourceInfo.Country,
		Language:     raw.SourceInfo.Language,
		Title:        title,
		Body:         body,
		Summary:      summary,
		Author:       author,
		PublishedAt:  publishedAt,
		URL:          url,
		CanonicalURL: url,
		WordCount:    len(strings.Fields(body)),
		ContentHash:  ContentHash(body),
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
