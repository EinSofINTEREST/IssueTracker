package storage

import (
	"context"
	"time"
)

// TargetType 은 파싱 규칙이 적용될 페이지 종류입니다 (이슈 #100).
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

// 호환성 — 기존 TargetTypeArticle 명칭이 코드/DB 에 잔존할 수 있어 별칭 유지.
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
//
// page (단일 컨텐츠 페이지) 용 필드와 list (링크-허브 페이지) 용 필드가 한 struct 에
// 함께 정의되지만, target_type 에 따라 사용되는 부분만 채워집니다 (JSONB nil 친화).
//
// 뉴스 / 블로그 / 제품 페이지 등 임의 웹페이지의 핵심 내용 추출에 일반화 (이슈 #100):
//   - Title       : 페이지 제목 (h1 등)
//   - MainContent : 핵심 본문 (article body / blog post / product description ...)
//   - Summary     : meta description 또는 별도 요약 영역
//   - Author      : 게시자/저자 (있을 때)
//   - PublishedAt : 게시 시각 selector (datetime attribute 권장)
//   - Category    : 섹션/카테고리 (뉴스 섹션 / 블로그 카테고리 / 제품 카테고리 등)
//   - Tags        : 태그 슬라이스
//   - Images      : 핵심 이미지 URL 슬라이스 (이전 ImageURLs)
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

	// LinkDiscovery (이슈 #139): 페이지 전체 <a href> 스캔 + URL 패턴 필터 모드.
	//
	// 설정 시 ParseLinks 가 ItemContainer 경로 대신 full-page discovery 로 동작합니다 —
	// 사이드바 / 추천 / 관련 기사 / 카테고리 메뉴 등 ItemContainer 가 놓치는 영역까지
	// 같은 fetch 비용으로 발견. nil 또는 비어있으면 기존 ItemContainer 경로 사용 (opt-in).
	//
	// 권장 활용: 사이트 전체 article URL 이 일관된 path 패턴을 가질 때
	//   (예: cnn = "^/\d{4}/\d{2}/\d{2}/", naver-news = "^/article/\d+/\d+").
	// noise 는 ExcludePatterns + MaxLinksPerPage + 다운스트림 URL dedup / rate limiter 가 흡수.
	LinkDiscovery *LinkDiscoveryConfig `json:"link_discovery,omitempty"`
}

// LinkDiscoveryConfig 는 full-page link discovery 모드의 설정입니다 (이슈 #139).
//
// LinkDiscoveryConfig drives the rule-based full-page <a href> discovery, replacing
// site-specific ItemContainer extraction with a generic URL-pattern filter.
//
// 모든 필드는 optional 이지만 ArticleURLPattern 이 비어있으면 discovery 모드 자체가 비활성화됩니다
// (= 기존 ItemContainer 경로로 fallback). pattern 만 채우면 최소 동작 가능.
type LinkDiscoveryConfig struct {
	// ArticleURLPattern: 통과 조건 (regex, RE2). 매칭되는 절대 URL 만 발행.
	// 빈 문자열이면 LinkDiscovery 자체가 disabled — 호출자는 ItemContainer 경로 사용.
	ArticleURLPattern string `json:"article_url_pattern"`

	// ExcludePatterns: 제외 조건 (substring, 대소문자 무시). 기본 제외 (javascript:/mailto:/login 등)
	// 외에 사이트별 광고 / share 링크 등 추가. pkg/links.WithExcludePatterns 로 직접 전달.
	ExcludePatterns []string `json:"exclude_patterns,omitempty"`

	// PathPrefixes: 허용 path prefix 필터 (regex 보조용). 비어있으면 전체 경로 허용.
	// regex 보다 빠른 1차 cutoff 로 활용 가능 (예: "/news/" prefix 만 통과 후 regex 정밀 매칭).
	PathPrefixes []string `json:"path_prefixes,omitempty"`

	// SameOriginOnly: true 면 page URL 과 scheme+host 가 동일한 링크만 통과 (외부 링크 차단).
	// 기본 true 권장 — 외부 링크 fetch 는 별도 정책으로 다뤄야 함.
	SameOriginOnly bool `json:"same_origin_only,omitempty"`

	// MaxLinksPerPage: 한 페이지에서 발행할 최대 링크 수 (0 = 무제한).
	// publish 폭증 방지 — backlog throttle (#124) 와 함께 2중 안전장치.
	// 권장 기본 200.
	MaxLinksPerPage int `json:"max_links_per_page,omitempty"`
}

// ParsingRuleRecord 는 parsing_rules 테이블의 단일 행입니다.
//
// ParsingRuleRecord represents a single row of the parsing_rules table.
type ParsingRuleRecord struct {
	ID          int64
	SourceName  string     // "naver" / "cnn"
	HostPattern string     // URL host 매칭 (예: "n.news.naver.com")
	TargetType  TargetType // "page" | "list"
	Version     int        // 활성 row 안에서 같은 (source, host, type) 의 최신 버전
	Enabled     bool
	Selectors   SelectorMap // JSONB — application 측 struct 로 직렬화
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ParsingRuleFilter 는 List 조회 시 필터 조건입니다.
type ParsingRuleFilter struct {
	SourceName  string     // 빈 문자열이면 전체
	HostPattern string     // 빈 문자열이면 전체
	TargetType  TargetType // 빈 문자열이면 전체
	OnlyEnabled bool       // true 면 enabled=true 만
	Limit       int        // 0 이면 기본값 (50)
	Offset      int
}

// ParsingRuleRepository 는 parsing_rules 테이블에 대한 데이터 접근 인터페이스입니다.
//
// ParsingRuleRepository is the data access interface for parsing_rules.
// All implementations must be goroutine-safe.
type ParsingRuleRepository interface {
	// Insert 는 새 규칙을 저장합니다. 자연키 충돌 시 ErrDuplicate 반환.
	// 성공 시 r.ID 가 채워집니다.
	Insert(ctx context.Context, r *ParsingRuleRecord) error

	// Update 는 ID 로 규칙을 갱신합니다. 존재하지 않으면 ErrNotFound 반환.
	// 갱신 가능 필드: Selectors, Enabled, Description (자연키는 변경 불가).
	Update(ctx context.Context, r *ParsingRuleRecord) error

	// GetByID 는 ID 로 규칙을 조회합니다.
	GetByID(ctx context.Context, id int64) (*ParsingRuleRecord, error)

	// FindActive 는 host + target_type 에 매칭되는 활성 규칙을 반환합니다 (RuleResolver 핫패스).
	// 같은 (host, type) 에 여러 활성 row 가 있다면 version DESC 순으로 첫 항목 반환.
	// 매칭 없으면 ErrNotFound.
	FindActive(ctx context.Context, host string, targetType TargetType) (*ParsingRuleRecord, error)

	// List 는 필터 조건에 맞는 규칙들을 반환합니다 (운영 대시보드용).
	List(ctx context.Context, filter ParsingRuleFilter) ([]*ParsingRuleRecord, error)

	// Delete 는 ID 로 규칙을 삭제합니다. 존재하지 않아도 nil 반환 (idempotent).
	Delete(ctx context.Context, id int64) error
}
