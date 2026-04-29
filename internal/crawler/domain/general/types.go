// Package general 은 임의 웹페이지 (뉴스/블로그/제품/일반 문서) 크롤링·파싱을 위한
// 도메인 중립 추상화를 제공합니다 (이슈 #100 / #139 통합).
//
// Package general provides domain-neutral abstractions for crawling arbitrary web pages
// (news, blogs, product pages, generic documents). 기존 news 도메인을 흡수합니다 —
// 모든 사이트는 본 패키지의 인터페이스를 구현하고, 파싱은 internal/crawler/parser/rule
// 의 단일 DB 규칙 기반 엔진이 처리합니다.
//
// Source-specific parsers (NaverParser/CNNParser/...) 는 더 이상 존재하지 않습니다.
// 새 사이트 지원 = parsing_rules 테이블에 row 추가.
package general

import (
	"context"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/parser"
)

// SourceCrawler 는 사이트별 크롤러가 구현해야 하는 도메인 중립 인터페이스입니다.
// core.Crawler 의 라이프사이클을 그대로 노출 — 추가 메소드는 두지 않습니다.
//
// SourceCrawler is the source-agnostic crawler interface — a thin alias of core.Crawler.
// 기존 news.NewsCrawler 의 FetchList/FetchArticle 은 chain handler + rule.Parser 가
// 흡수했으므로 본 인터페이스에서 제거.
type SourceCrawler interface {
	core.Crawler
}

// Fetcher 는 단일 URL 의 raw HTML 을 가져오는 저수준 추상화입니다.
// goquery / chromedp 어댑터가 본 인터페이스를 구현 — chain handler 는 이 인터페이스에만 의존 (DIP).
//
// Fetcher fetches raw HTML for a URL. goquery / chromedp adapters implement this.
type Fetcher interface {
	Fetch(ctx context.Context, target core.Target) (*core.RawContent, error)
}

// RSSFetcher 는 RSS/Atom 피드를 가져와 파싱된 page 목록을 반환합니다.
//
// RSSFetcher fetches and parses an RSS/Atom feed into a list of pages.
// 기존 NewsRSSFetcher 의 *NewsArticle 반환을 도메인 중립 *parser.Page 로 일반화.
type RSSFetcher interface {
	FetchFeed(ctx context.Context, feedURL string) ([]*parser.Page, error)
}

// JobPublisher 는 카테고리/목록 페이지에서 발견된 URL 을 다음 CrawlJob 으로 연결하는 인터페이스입니다.
// publisher.Publisher 가 본 인터페이스를 구현하며, ChainHandler 에 주입됩니다.
//
// JobPublisher dispatches CrawlJobs discovered from list/category pages.
type JobPublisher interface {
	Publish(ctx context.Context, crawlerName string, urls []string, targetType core.TargetType, timeout time.Duration) error
}
