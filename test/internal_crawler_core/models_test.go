package core_test

import (
  core "ecoscrapper/internal/crawler/core"

  "testing"
  "time"

  "github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
  config := core.DefaultConfig()

  assert.Equal(t, 30*time.Second, config.Timeout)
  assert.Equal(t, 100, config.MaxIdleConns)
  assert.Equal(t, 10, config.MaxConnsPerHost)
  assert.Contains(t, config.UserAgent, "EcoScrapper")
  assert.Equal(t, 100, config.RequestsPerHour)
  assert.Equal(t, 10, config.BurstSize)
  assert.Equal(t, 3, config.MaxRetries)
  assert.Equal(t, 1*time.Second, config.RetryBackoff)
}

func TestSourceType_Constants(t *testing.T) {
  assert.Equal(t, core.SourceType("news"), core.SourceTypeNews)
  assert.Equal(t, core.SourceType("community"), core.SourceTypeCommunity)
  assert.Equal(t, core.SourceType("social"), core.SourceTypeSocial)
}

func TestTargetType_Constants(t *testing.T) {
  assert.Equal(t, core.TargetType("feed"), core.TargetTypeFeed)
  assert.Equal(t, core.TargetType("sitemap"), core.TargetTypeSitemap)
  assert.Equal(t, core.TargetType("article"), core.TargetTypeArticle)
  assert.Equal(t, core.TargetType("category"), core.TargetTypeCategory)
}

func TestSourceInfo_Creation(t *testing.T) {
  source := core.SourceInfo{
    Country:  "US",
    Type:     core.SourceTypeNews,
    Name:     "CNN",
    BaseURL:  "https://cnn.com",
    Language: "en",
  }

  assert.Equal(t, "US", source.Country)
  assert.Equal(t, core.SourceTypeNews, source.Type)
  assert.Equal(t, "CNN", source.Name)
  assert.Equal(t, "https://cnn.com", source.BaseURL)
  assert.Equal(t, "en", source.Language)
}

func TestTarget_Creation(t *testing.T) {
  target := core.Target{
    URL:  "https://example.com/article",
    Type: core.TargetTypeArticle,
    Metadata: map[string]interface{}{
      "category": "politics",
    },
  }

  assert.Equal(t, "https://example.com/article", target.URL)
  assert.Equal(t, core.TargetTypeArticle, target.Type)
  assert.Equal(t, "politics", target.Metadata["category"])
}

func TestRawContent_Creation(t *testing.T) {
  now := time.Now()
  source := core.SourceInfo{
    Country:  "US",
    Type:     core.SourceTypeNews,
    Name:     "Test",
    BaseURL:  "https://test.com",
    Language: "en",
  }

  raw := core.RawContent{
    ID:         "test-123",
    SourceInfo: source,
    FetchedAt:  now,
    URL:        "https://test.com/article",
    HTML:       "<html><body>test</body></html>",
    StatusCode: 200,
    Headers: map[string]string{
      "Content-Type": "text/html",
    },
    Metadata: map[string]interface{}{
      "redirect": false,
    },
  }

  assert.Equal(t, "test-123", raw.ID)
  assert.Equal(t, source, raw.SourceInfo)
  assert.Equal(t, now, raw.FetchedAt)
  assert.Equal(t, "https://test.com/article", raw.URL)
  assert.Contains(t, raw.HTML, "test")
  assert.Equal(t, 200, raw.StatusCode)
  assert.Equal(t, "text/html", raw.Headers["Content-Type"])
  assert.False(t, raw.Metadata["redirect"].(bool))
}

func TestContent_Creation(t *testing.T) {
  now := time.Now()
  updatedAt := now.Add(1 * time.Hour)

  content := core.Content{
    ID:       "article-123",
    SourceID: "source-456",
    Country:  "US",
    Language: "en",
    Title:    "Test core.Content",
    Body:     "This is a test article body.",
    Summary:  "Test summary",
    Author:   "John Doe",
    PublishedAt: now,
    UpdatedAt:   &updatedAt,
    Category:    "Technology",
    Tags:        []string{"tech", "news"},
    URL:         "https://example.com/article",
    CanonicalURL: "https://example.com/canonical",
    ImageURLs:    []string{"https://example.com/image.jpg"},
    ContentHash:  "abc123",
    WordCount:    6,
    CreatedAt:    now,
  }

  assert.Equal(t, "article-123", content.ID)
  assert.Equal(t, "source-456", content.SourceID)
  assert.Equal(t, "US", content.Country)
  assert.Equal(t, "en", content.Language)
  assert.Equal(t, "Test core.Content", content.Title)
  assert.Contains(t, content.Body, "test article")
  assert.Equal(t, "Test summary", content.Summary)
  assert.Equal(t, "John Doe", content.Author)
  assert.Equal(t, now, content.PublishedAt)
  assert.NotNil(t, content.UpdatedAt)
  assert.Equal(t, updatedAt, *content.UpdatedAt)
  assert.Equal(t, "Technology", content.Category)
  assert.Len(t, content.Tags, 2)
  assert.Contains(t, content.Tags, "tech")
  assert.Equal(t, "https://example.com/article", content.URL)
  assert.Equal(t, "https://example.com/canonical", content.CanonicalURL)
  assert.Len(t, content.ImageURLs, 1)
  assert.Equal(t, "abc123", content.ContentHash)
  assert.Equal(t, 6, content.WordCount)
  assert.Equal(t, now, content.CreatedAt)
}
