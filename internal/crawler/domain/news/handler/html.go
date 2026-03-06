// Package handler provides content conversion utilities for the news domain.
//
// handler 패키지는 뉴스 도메인에서 크롤링한 결과를 core.Content로 변환하는 유틸리티를 제공합니다.
// news 패키지와의 순환 임포트를 피하기 위해 NewsArticle 대신 Article DTO를 사용합니다.
package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage"
)

// Article은 뉴스 기사 파싱 결과를 담는 DTO입니다.
// news.NewsArticle과 core.Content 사이의 변환 매개체 역할을 합니다.
//
// Article is a DTO holding the result of parsing a news article.
// It acts as an intermediary between news.NewsArticle and core.Content.
type Article struct {
	Title       string
	Body        string
	Summary     string
	Author      string
	URL         string
	Category    string
	Tags        []string
	ImageURLs   []string
	PublishedAt time.Time
}

// ConvertArticle은 파싱된 Article과 RawContent를 core.Content로 변환합니다.
// URL 우선순위: article.URL → raw.URL
//
// ConvertArticle converts a parsed Article and RawContent into a core.Content.
func ConvertArticle(article Article, raw *core.RawContent) *core.Content {
	url := article.URL
	if url == "" {
		url = raw.URL
	}

	return &core.Content{
		ID:           ContentID(url),
		SourceID:     raw.SourceInfo.Name,
		SourceType:   raw.SourceInfo.Type,
		Country:      raw.SourceInfo.Country,
		Language:     raw.SourceInfo.Language,
		Title:        article.Title,
		Body:         article.Body,
		Summary:      article.Summary,
		Author:       article.Author,
		PublishedAt:  article.PublishedAt,
		Category:     article.Category,
		Tags:         article.Tags,
		URL:          url,
		CanonicalURL: url,
		ImageURLs:    article.ImageURLs,
		WordCount:    len(strings.Fields(article.Body)),
		ContentHash:  ContentHash(article.Body),
		CreatedAt:    time.Now(),
	}
}

// ArticleToRecord는 Article과 RawContent를 storage.NewsArticleRecord로 변환합니다.
// URL은 chain이 확정한 raw.URL을 우선 사용합니다.
//
// ArticleToRecord converts an Article and RawContent into a storage.NewsArticleRecord.
func ArticleToRecord(article Article, raw *core.RawContent) *storage.NewsArticleRecord {
	record := &storage.NewsArticleRecord{
		SourceName: raw.SourceInfo.Name,
		SourceType: string(raw.SourceInfo.Type),
		Country:    raw.SourceInfo.Country,
		Language:   raw.SourceInfo.Language,
		URL:        raw.URL,
		Title:      article.Title,
		Body:       article.Body,
		Summary:    article.Summary,
		Author:     article.Author,
		Category:   article.Category,
		Tags:       article.Tags,
		ImageURLs:  article.ImageURLs,
		FetchedAt:  raw.FetchedAt,
	}

	if !article.PublishedAt.IsZero() {
		t := article.PublishedAt
		record.PublishedAt = &t
	}

	return record
}

// ContentID는 URL의 SHA-256 해시 앞 16바이트를 hex 문자열로 반환합니다.
//
// ContentID returns the first 16 bytes of SHA-256(url) as a hex string.
func ContentID(url string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:16])
}

// ContentHash는 본문의 SHA-256 해시를 hex 문자열로 반환합니다.
//
// ContentHash returns the full SHA-256 hash of body text as a hex string.
func ContentHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}
