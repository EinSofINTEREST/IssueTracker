package model

import "time"

// TargetType 은 파싱 규칙이 적용될 페이지 종류입니다.
//
// TargetType discriminates rules between article pages and list/category pages.
type TargetType string

const (
	// TargetTypePage: 단일 컨텐츠 페이지 — Title/MainContent/Author 등 추출.
	// 뉴스 기사 / 블로그 포스트 / 제품 페이지 / 일반 문서 모두 포함.
	TargetTypePage TargetType = "page"
	// TargetTypeList: 링크-허브 페이지 — 카테고리/목록/sitemap 등 LinkItem 들 추출.
	TargetTypeList TargetType = "list"
)

// TargetTypeArticle 은 TargetTypePage 의 호환 별칭입니다 — 기존 코드/DB 에 잔존할 수 있어 유지.
//
// Deprecated: TargetTypePage 사용 권장 (도메인 일반화).
const TargetTypeArticle = TargetTypePage

// FieldSelector 는 단일 필드의 추출 규칙입니다.
//
// FieldSelector defines how to extract one field from HTML.
//
//   - CSS: goquery selector (예: "h1.article-title", "div.author > a")
//   - Attribute: 빈 문자열이면 element 의 .Text(), 그 외엔 attribute 값
//     (예: "href" / "src" / "datetime" / "content")
//   - Multi: 여러 element 매칭 시 동작
//   - false: 첫 element 만 반환 (Title 등 단일 값)
//   - true: 모든 element 의 결과를 합침/배열 반환 (Tags / ImageURLs / Body 의 다중 단락 등)
type FieldSelector struct {
	CSS       string `json:"css"`
	Attribute string `json:"attribute,omitempty"`
	Multi     bool   `json:"multi,omitempty"`
}

// SelectorMap 은 page/list 페이지에서 추출할 모든 필드의 selector 모음입니다.
//
// SelectorMap holds selectors for every extractable field. Nil entries mean
// "field not configured" — parser 는 해당 필드를 빈 값으로 두고 계속 진행합니다.
type SelectorMap struct {
	// page (단일 컨텐츠 페이지) 용
	Title       *FieldSelector `json:"title,omitempty"`
	MainContent *FieldSelector `json:"main_content,omitempty"` // 핵심 본문 (article body / post / description 등)
	Summary     *FieldSelector `json:"summary,omitempty"`
	Author      *FieldSelector `json:"author,omitempty"`
	PublishedAt *FieldSelector `json:"published_at,omitempty"`
	Category    *FieldSelector `json:"category,omitempty"`
	Tags        *FieldSelector `json:"tags,omitempty"`
	Images      *FieldSelector `json:"images,omitempty"`

	// list (링크-허브 페이지) 용 — 각 item 의 link/title/snippet 추출
	ItemContainer *FieldSelector `json:"item_container,omitempty"` // 각 item 의 root element selector
	ItemLink      *FieldSelector `json:"item_link,omitempty"`      // ItemContainer 내 link selector (attribute=href 권장)
	ItemTitle     *FieldSelector `json:"item_title,omitempty"`
	ItemSnippet   *FieldSelector `json:"item_snippet,omitempty"` // 짧은 요약/설명 (있을 때)

	// LinkDiscovery: 페이지 전체 <a href> 스캔 + 정책 기반 필터 모드.
	LinkDiscovery *LinkDiscoveryConfig `json:"link_discovery,omitempty"`
}

// LinkDiscoveryConfig 는 full-page link discovery 모드의 설정입니다.
type LinkDiscoveryConfig struct {
	ArticleURLPattern string   `json:"article_url_pattern"`
	ExcludePatterns   []string `json:"exclude_patterns,omitempty"`
	PathPrefixes      []string `json:"path_prefixes,omitempty"`
	SameOriginOnly    bool     `json:"same_origin_only,omitempty"`
	MaxLinksPerPage   int      `json:"max_links_per_page,omitempty"`
}

// ParserRuleRecord 는 parser_rules 테이블의 단일 행입니다.
type ParserRuleRecord struct {
	ID          int64
	SourceName  string
	HostPattern string
	PathPattern string
	TargetType  TargetType
	Version     int
	Enabled     bool
	Selectors   SelectorMap
	Confidence  map[string]FieldConfidence
	Description string
	PageType    string
	// Article 은 룰이 적용되는 페이지가 뉴스 기사 본문인지 표시합니다 (이슈 #421).
	Article bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// FieldConfidence 는 단일 필드의 추출 신뢰도.
type FieldConfidence struct {
	HitRate     float64 `json:"hit_rate"`
	SampleCount int     `json:"sample_count"`
}

// ParserRuleFilter 는 List 조회 시 필터 조건입니다.
type ParserRuleFilter struct {
	SourceName  string
	HostPattern string
	TargetType  TargetType
	OnlyEnabled bool

	// Article: nil 이면 미적용 (article 무관 전체).
	Article *bool

	Limit  int
	Offset int
}
