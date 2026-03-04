// Package news는 뉴스 소스 크롤링을 위한 도메인 인터페이스를 제공합니다.
//
// Package news provides domain interfaces for crawling news sources.
// All source-specific crawlers implement NewsCrawler.
// Chain-of-responsibility handlers depend on NewsFetcher/NewsRSSFetcher for DIP compliance.
package news

import (
	"context"
	"time"

	"issuetracker/internal/crawler/core"
)

// NewsArticle은 파싱된 뉴스 기사를 나타냅니다.
// core.Content보다 뉴스 도메인에 특화된 값 객체입니다.
//
// NewsArticle is a news-domain value object representing a parsed article.
type NewsArticle struct {
	Title       string
	Body        string
	Summary     string
	Author      string
	URL         string
	PublishedAt time.Time
	Category    string
	Tags        []string
	ImageURLs   []string
}

// NewsItem은 목록 페이지에서 추출한 기사 링크와 요약 정보입니다.
//
// NewsItem represents an article link extracted from a list or category page.
type NewsItem struct {
	URL     string
	Title   string
	Summary string
}

// NewsCrawler는 뉴스 소스 크롤러가 구현해야 하는 도메인 인터페이스입니다.
// core.Crawler를 임베드하여 기본 생명주기와 메타데이터를 포함합니다.
//
// NewsCrawler extends core.Crawler with news-specific capabilities.
// Implementations embed lifecycle management from core.Crawler.
type NewsCrawler interface {
	core.Crawler

	// FetchList는 카테고리/목록 페이지에서 기사 항목 목록을 가져옵니다.
	// 반환된 NewsItem 슬라이스는 이후 FetchArticle로 개별 처리됩니다.
	FetchList(ctx context.Context, target core.Target) ([]NewsItem, error)

	// FetchArticle은 단일 기사 URL에서 전체 기사 내용을 가져옵니다.
	FetchArticle(ctx context.Context, url string) (*NewsArticle, error)
}

// NewsFetcher는 단일 URL에서 RawContent를 가져오는 저수준 추상화입니다.
// Chain of Responsibility 핸들러가 이 인터페이스에 의존하여 DIP를 충족합니다.
// goquery 어댑터와 browser 어댑터가 이 인터페이스를 구현합니다.
//
// NewsFetcher is the low-level fetch abstraction used by chain handlers.
// Concrete adapters (GoqueryFetcher, BrowserFetcher) implement this interface.
type NewsFetcher interface {
	// Fetch는 주어진 target URL에서 raw HTML을 가져옵니다.
	Fetch(ctx context.Context, target core.Target) (*core.RawContent, error)
}

// NewsRSSFetcher는 RSS/Atom 피드를 가져와 파싱하는 추상화입니다.
// RSSFetcher가 이 인터페이스를 구현합니다.
//
// NewsRSSFetcher abstracts RSS/Atom feed fetching and parsing.
type NewsRSSFetcher interface {
	// FetchFeed는 피드 URL에서 기사 목록을 가져오고 파싱합니다.
	FetchFeed(ctx context.Context, feedURL string) ([]*NewsArticle, error)
}

// NewsArticleParser는 RawContent를 NewsArticle로 파싱하는 추상화입니다.
// 각 뉴스 소스의 HTML 구조에 맞는 구체 파서가 이 인터페이스를 구현합니다.
//
// NewsArticleParser parses a single article page from raw HTML content.
type NewsArticleParser interface {
	// ParseArticle은 단일 기사 페이지의 RawContent를 파싱합니다.
	ParseArticle(raw *core.RawContent) (*NewsArticle, error)
}

// NewsListParser는 목록 페이지에서 기사 링크를 추출하는 추상화입니다.
//
// NewsListParser extracts article links from a category or list page.
type NewsListParser interface {
	// ParseList는 목록 페이지에서 NewsItem 슬라이스를 추출합니다.
	ParseList(raw *core.RawContent) ([]NewsItem, error)
}
