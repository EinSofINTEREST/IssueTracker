package handler

import (
	"strings"
	"time"

	"issuetracker/internal/crawler/core"
)

// ConvertRSSArticles는 RSS RawContent의 metadata에서 기사 목록을 추출하여 Content 슬라이스로 변환합니다.
// metadata["rss_articles"]가 없거나 타입이 맞지 않으면 nil을 반환합니다.
//
// ConvertRSSArticles extracts articles from RSS metadata and converts them to Content slice.
// Returns nil if metadata["rss_articles"] is absent or has an unexpected type.
func ConvertRSSArticles(raw *core.RawContent) []*core.Content {
	items, ok := raw.Metadata["rss_articles"].([]map[string]interface{})
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

// rssItemToContent는 RSS 기사 맵을 core.Content로 변환합니다.
// url이 비어 있으면 nil을 반환합니다.
func rssItemToContent(item map[string]interface{}, raw *core.RawContent) *core.Content {
	url, _ := item["url"].(string)
	if url == "" {
		return nil
	}

	title, _ := item["title"].(string)
	body, _ := item["body"].(string)
	author, _ := item["author"].(string)

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
		Author:       author,
		PublishedAt:  publishedAt,
		URL:          url,
		CanonicalURL: url,
		WordCount:    len(strings.Fields(body)),
		ContentHash:  ContentHash(body),
		CreatedAt:    time.Now(),
	}
}
