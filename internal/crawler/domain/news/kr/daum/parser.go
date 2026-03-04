package daum

import (
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
)

// DaumParser는 다음 뉴스 기사와 목록 페이지를 파싱합니다.
// news.NewsArticleParser와 news.NewsListParser를 구현합니다.
//
// DaumParser implements NewsArticleParser and NewsListParser for Daum News.
type DaumParser struct {
	config DaumConfig
}

// NewDaumParser는 새로운 DaumParser를 생성합니다.
func NewDaumParser(config DaumConfig) *DaumParser {
	return &DaumParser{config: config}
}

// ParseArticle은 다음 뉴스 기사 페이지를 파싱합니다.
// title 또는 body가 비어있으면 PARSE_002 에러를 반환합니다.
func (p *DaumParser) ParseArticle(raw *core.RawContent) (*news.NewsArticle, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
	if err != nil {
		return nil, core.NewParseError("PARSE_001", "failed to parse daum article html", raw.URL, err)
	}

	// extractBody가 이미지를 제거하기 전에 먼저 추출
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
		return nil, core.NewParseError("PARSE_002", "missing required fields in daum article", raw.URL, nil)
	}

	return article, nil
}

// ParseList는 다음 뉴스 목록 페이지에서 기사 링크를 추출합니다.
func (p *DaumParser) ParseList(raw *core.RawContent) ([]news.NewsItem, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
	if err != nil {
		return nil, core.NewParseError("PARSE_001", "failed to parse daum list html", raw.URL, err)
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
// <span class="txt_info">의 첫 번째 요소 텍스트를 그대로 반환합니다.
// 예: "이승환 기자 이정후 기자 임윤지 기자"
func (p *DaumParser) extractAuthors(doc *goquery.Document) string {
	return strings.TrimSpace(doc.Find(p.config.ArticleSelectors["author"]).First().Text())
}

// extractBody는 기사 본문 텍스트를 추출합니다.
// 광고, 관련 기사 영역, 불필요한 태그를 제거합니다.
func (p *DaumParser) extractBody(doc *goquery.Document) string {
	bodyDiv := doc.Find(p.config.ArticleSelectors["body"])
	bodyDiv.Find("figure, .article_ad, .link_news, .related_article").Remove()

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
func (p *DaumParser) extractTags(doc *goquery.Document) []string {
	sel := p.config.ArticleSelectors["tags"]
	if sel == "" {
		return nil
	}

	var tags []string
	doc.Find(sel).Each(func(_ int, s *goquery.Selection) {
		tag := strings.TrimSpace(s.Text())
		if tag != "" {
			tags = append(tags, tag)
		}
	})
	return tags
}

// extractImageURLs는 기사 내 이미지 URL 목록을 추출합니다.
// .article_view 하위 img 태그의 src 속성을 수집합니다.
func (p *DaumParser) extractImageURLs(doc *goquery.Document) []string {
	zone := doc.Find(p.config.ArticleSelectors["image_urls"])

	var urls []string
	zone.Each(func(_ int, s *goquery.Selection) {
		// data-src 우선 (lazy loading), 없으면 src 사용
		src := s.AttrOr("data-src", "")
		if src == "" {
			src = s.AttrOr("src", "")
		}
		if src != "" && !strings.HasPrefix(src, "data:") {
			urls = append(urls, src)
		}
	})
	return urls
}

// extractCategory는 다음 뉴스 카테고리를 추출합니다.
// .info_cate 텍스트를 반환합니다.
func (p *DaumParser) extractCategory(doc *goquery.Document) string {
	category := doc.Find(p.config.ArticleSelectors["category"]).First().Text()
	return strings.TrimSpace(category)
}

// extractDate는 기사 날짜를 파싱합니다.
// <span class="txt_info">의 두 번째 요소 텍스트를 사용합니다.
// 다음 뉴스 날짜 형식: "2026. 3. 3. 오전 9:30" 또는 "2026.03.03. 14:30:00" (KST)
// 파싱 실패 시 현재 시간을 반환합니다.
func (p *DaumParser) extractDate(doc *goquery.Document) time.Time {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		return time.Now().UTC()
	}

	dateStr := strings.TrimSpace(doc.Find(p.config.ArticleSelectors["date"]).Eq(1).Text())
	if dateStr == "" {
		return time.Now().UTC()
	}

	// 형식 1: "2026. 3. 3. 오전 9:30" (12시간제, 한국어)
	if t := parseDaumKoreanDate(dateStr, loc); !t.IsZero() {
		return t.UTC()
	}

	// 형식 2: "2026.03.03. 14:30:00" (24시간제)
	if t, err := time.ParseInLocation("2006.01.02. 15:04:05", dateStr, loc); err == nil {
		return t.UTC()
	}

	// 형식 3: "2026-03-03 14:30:00"
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", dateStr, loc); err == nil {
		return t.UTC()
	}

	return time.Now().UTC()
}

// parseDaumKoreanDate는 "2026. 3. 3. 오전 9:30" 형식의 날짜를 파싱합니다.
// 파싱 실패 시 zero time을 반환합니다.
func parseDaumKoreanDate(s string, loc *time.Location) time.Time {
	// "오전"/"오후" 처리 후 24시간제로 변환
	isPM := strings.Contains(s, "오후")
	s = strings.ReplaceAll(s, "오전", "")
	s = strings.ReplaceAll(s, "오후", "")
	s = strings.Join(strings.Fields(s), " ")

	// "2026. 3. 3. 9:30" 형식 파싱
	t, err := time.ParseInLocation("2006. 1. 2. 15:04", s, loc)
	if err != nil {
		return time.Time{}
	}

	if isPM && t.Hour() < 12 {
		t = t.Add(12 * time.Hour)
	}
	if !isPM && t.Hour() == 12 {
		// 오전 12시 → 0시
		t = t.Add(-12 * time.Hour)
	}

	return t
}
