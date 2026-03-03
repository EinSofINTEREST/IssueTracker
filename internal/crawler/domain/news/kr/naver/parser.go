package naver

import (
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
)

// NaverParser는 네이버 뉴스 기사와 목록 페이지를 파싱합니다.
// news.NewsArticleParser와 news.NewsListParser를 구현합니다.
//
// NaverParser implements NewsArticleParser and NewsListParser for Naver News.
type NaverParser struct {
	config NaverConfig
}

// NewNaverParser는 새로운 NaverParser를 생성합니다.
func NewNaverParser(config NaverConfig) *NaverParser {
	return &NaverParser{config: config}
}

// ParseArticle은 네이버 뉴스 기사 페이지를 파싱합니다.
// title 또는 body가 비어있으면 PARSE_002 에러를 반환합니다.
func (p *NaverParser) ParseArticle(raw *core.RawContent) (*news.NewsArticle, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
	if err != nil {
		return nil, core.NewParseError("PARSE_001", "failed to parse naver article html", raw.URL, err)
	}

	// extractBody가 이미지 컨테이너(span.end_photo_org)를 제거하기 전에 먼저 추출
	imageURLs := p.extractImageURLs(doc)

	article := &news.NewsArticle{
		URL:         raw.URL,
		Title:       strings.TrimSpace(doc.Find(p.config.ArticleSelectors["title"]).First().Text()),
		Author:      p.extractAuthors(doc),
		Body:        p.extractBody(doc),
		Tags:        p.extractTags(doc),
		ImageURLs:   imageURLs,
		PublishedAt: p.extractDate(doc),
		Category:    p.extractCategory(doc),
	}

	if article.Title == "" || article.Body == "" {
		return nil, core.NewParseError("PARSE_002", "missing required fields in naver article", raw.URL, nil)
	}

	return article, nil
}

// ParseList는 네이버 뉴스 목록 페이지에서 기사 링크를 추출합니다.
func (p *NaverParser) ParseList(raw *core.RawContent) ([]news.NewsItem, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
	if err != nil {
		return nil, core.NewParseError("PARSE_001", "failed to parse naver list html", raw.URL, err)
	}

	var items []news.NewsItem

	doc.Find(p.config.ListSelectors["item"]).Each(func(_ int, s *goquery.Selection) {
		linkEl := s.Find(p.config.ListSelectors["link"])
		href, exists := linkEl.Attr("href")
		if !exists || href == "" {
			return
		}

		items = append(items, news.NewsItem{
			URL:     href,
			Title:   strings.TrimSpace(linkEl.Text()),
			Summary: strings.TrimSpace(s.Find(p.config.ListSelectors["summary"]).Text()),
		})
	})

	return items, nil
}

// extractAuthors는 기자 이름을 추출합니다.
// 복수 기자를 모두 수집하여 ", "로 구분한 문자열을 반환합니다.
func (p *NaverParser) extractAuthors(doc *goquery.Document) string {
	var names []string
	doc.Find(p.config.ArticleSelectors["author"]).Each(func(_ int, s *goquery.Selection) {
		name := strings.TrimSpace(s.Text())
		if name != "" {
			names = append(names, name)
		}
	})
	return strings.Join(names, ", ")
}

// extractBody는 기사 본문 텍스트를 추출합니다.
// 광고, 사진 캡션, 불필요한 태그를 제거합니다.
func (p *NaverParser) extractBody(doc *goquery.Document) string {
	bodyDiv := doc.Find(p.config.ArticleSelectors["body"])
	bodyDiv.Find("span.end_photo_org, div.ad_area, strong.media_end_summary").Remove()

	var parts []string
	bodyDiv.Find("p").Each(func(_ int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text != "" {
			parts = append(parts, text)
		}
	})

	// <p> 태그가 없으면 div 전체 텍스트 사용
	if len(parts) == 0 {
		text := strings.TrimSpace(bodyDiv.Text())
		if text != "" {
			parts = append(parts, text)
		}
	}

	return strings.Join(parts, "\n")
}

// extractTags는 태그 목록을 추출합니다.
// 네이버 뉴스는 별도의 태그 영역이 없어 nil을 반환합니다.
func (p *NaverParser) extractTags(doc *goquery.Document) []string {
	sel := p.config.ArticleSelectors["tags"]
	if sel == "" {
		return nil
	}

	zone := doc.Find(sel)
	var tags []string
	zone.Find("div > ul > li > a").Each(func(_ int, s *goquery.Selection) {
		tag := strings.TrimSpace(s.Text())
		if tag != "" {
			tags = append(tags, tag)
		}
	})
	return tags
}

// extractImageURLs는 기사 내 이미지 URL 목록을 추출합니다.
// <span class="end_photo_org"> 하위의 div > div > img의 src 속성을 수집합니다.
// lazy loading으로 인해 img 태그가 여러 개 존재할 수 있으므로 모두 수집하여 반환합니다.
func (p *NaverParser) extractImageURLs(doc *goquery.Document) []string {
	zone := doc.Find(p.config.ArticleSelectors["image_urls"])

	var urls []string
	zone.Find("div > div > img").Each(func(_ int, s *goquery.Selection) {
		src, exists := s.Attr("src")
		if exists && src != "" {
			urls = append(urls, src)
		}
	})
	return urls
}

// extractCategory는 네이버 뉴스 카테고리를 추출합니다.
// <ul class="Nlnb_menu_list"> 아래의 li.is_active > a > span 내용을 반환합니다.
func (p *NaverParser) extractCategory(doc *goquery.Document) string {
	category := doc.Find(p.config.ArticleSelectors["category"]).First().Text()
	return strings.TrimSpace(category)
}

// extractDate는 기사 날짜를 파싱합니다.
// <div class="media_end_head_info_datestamp_bunch"> 하위 span의 data-date-time 속성에서 추출합니다.
// 파싱 실패 시 현재 시간을 반환합니다.
func (p *NaverParser) extractDate(doc *goquery.Document) time.Time {
	dateStr, exists := doc.Find(p.config.ArticleSelectors["date"]).Attr("data-date-time")
	if !exists || dateStr == "" {
		return time.Now().UTC()
	}

	// 네이버 날짜 형식: "2026-03-02 14:54:16" (KST)
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		return time.Now().UTC()
	}

	t, err := time.ParseInLocation("2006-01-02 15:04:05", dateStr, loc)
	if err != nil {
		return time.Now().UTC()
	}

	// UTC로 변환하여 반환
	return t.UTC()
}
