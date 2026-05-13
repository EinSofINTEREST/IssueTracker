package storage

import (
	"context"
	"time"
)

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
//
// page (단일 컨텐츠 페이지) 용 필드와 list (링크-허브 페이지) 용 필드가 한 struct 에
// 함께 정의되지만, target_type 에 따라 사용되는 부분만 채워집니다 (JSONB nil 친화).
//
// 뉴스 / 블로그 / 제품 페이지 등 임의 웹페이지의 핵심 내용 추출에 일반화:
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

	// LinkDiscovery: 페이지 전체 <a href> 스캔 + 정책 기반 필터 모드.
	//
	// 설정 시 ParseLinks 가 ItemContainer 경로 대신 full-page discovery 로 동작합니다 —
	// 사이드바 / 추천 / 관련 기사 / 카테고리 메뉴 등 ItemContainer 가 놓치는 영역까지
	// 같은 fetch 비용으로 발견. **nil 일 때만** 기존 ItemContainer 경로로 fallback —
	// 객체가 채워져 있으면 ArticleURLPattern 이 빈 문자열이어도 discovery 모드 (all-pass).
	//
	// 권장 활용: 사이트 전체에서 모든 의미 있는 글을 발견하고자 할 때.
	// noise 는 ExcludePatterns + MaxLinksPerPage + 다운스트림 URL dedup / rate limiter 가 흡수.
	LinkDiscovery *LinkDiscoveryConfig `json:"link_discovery,omitempty"`
}

// LinkDiscoveryConfig 는 full-page link discovery 모드의 설정입니다.
//
// LinkDiscoveryConfig drives the rule-based full-page <a href> discovery, replacing
// site-specific ItemContainer extraction with a generic policy-based filter.
//
// 모든 필드는 optional. 본 객체가 SelectorMap.LinkDiscovery 에 채워져 있으면 ParseLinks 는
// discovery 모드로 동작합니다 — ArticleURLPattern 이 빈 문자열이어도 all-pass 모드로 진입.
// ItemContainer fallback 은 LinkDiscovery 자체가 nil 일 때만.
type LinkDiscoveryConfig struct {
	// ArticleURLPattern: 통과 조건 (regex, RE2). 매칭되는 절대 URL 만 발행.
	// 빈 문자열이면 all-pass 모드 — ExcludePatterns / SameOriginOnly / MaxLinksPerPage 만으로 필터링.
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
	//
	// 우선순위 정책:
	//   1. same-origin (raw.URL host 와 동일) 링크는 cap 무시하고 모두 통과
	//   2. cross-origin 은 잔여 슬롯 (cap - len(same)) 만큼 무작위 sample
	//   3. 0 (무제한) 이면 same + cross 모두 통과
	//
	// 사이트 자체 컨텐츠 (same) 는 noise 적고 가치 높음 — 모두 발행 가치.
	// 외부 링크 (cross) 는 광고/제휴 noise 많음 — cap 통제 + 무작위 sample 로
	// 페이지 상단 sponsored slot 등 특정 영역 편향 회피.
	//
	// publish 폭증 방지 — backlog throttle (#124) 와 함께 2중 안전장치. 권장 기본 200.
	MaxLinksPerPage int `json:"max_links_per_page,omitempty"`
}

// ParserRuleRecord 는 parser_rules 테이블의 단일 행입니다.
//
// ParserRuleRecord represents a single row of the parser_rules table.
type ParserRuleRecord struct {
	ID          int64
	SourceName  string     // "naver" / "cnn"
	HostPattern string     // URL host 매칭 (예: "n.news.naver.com")
	PathPattern string     // URL path regex (RE2). "" 면 모든 path 매칭.
	TargetType  TargetType // "page" | "list"
	Version     int        // 활성 row 안에서 같은 (source, host, path, type) 의 최신 버전
	Enabled     bool
	Selectors   SelectorMap // JSONB — application 측 struct 로 직렬화

	// Confidence 는 필드별 selector 추출 신뢰도 metadata 입니다.
	//
	// LLM 자동 생성 룰이 INSERT 시점에 각 selector 를 sample HTML 에 적용하여 hit_rate 계산.
	// 하류 validator 는 본 metadata 로 host 별 차별화된 정책 적용 가능 (예: published_at hit_rate=0
	// 인 host 는 published_at 누락을 reject 하지 않음 — sub-issue 로 분리).
	//
	// nil 또는 빈 map 은 "metadata 미상" 의미 — 기존 룰 / 운영자 manual 등록 룰에 대해
	// validator 는 보수적 default 정책 (모든 필드 reliable 가정) 적용.
	Confidence  map[string]FieldConfidence
	Description string

	// PageType 은 페이지 도메인 분류입니다 (news/community/info/commercial/paper/other).
	//
	// claudegen multi-step extraction 이 LLM 응답에서 추출 (이슈 #326). 빈 문자열은 미분류 —
	// non-claudegen 경로 (Gemini Flash 등) 또는 운영자 manual 등록 룰. 정보 신뢰도 시스템
	// (별도 후속 이슈) 의 1차 입력.
	PageType string

	// Article 은 룰이 적용되는 페이지가 뉴스 기사 본문인지 표시합니다 (이슈 #421).
	//
	//   - true  : 순수 뉴스 기사 본문 페이지 — title + main_content + published_at 추출 대상.
	//             validate / classifier 가 strict 검증 / 분류 적용.
	//   - false : 뉴스 인덱스 / 이미지 / 멀티미디어 / 기타 비-article 페이지. 다운스트림이
	//             해당 룰에 대해 검증 완화 또는 처리 분기.
	//
	// 기본값 false — operator 명시적 opt-in. PageType 과 직교 (예: PageType=news + article=true
	// 가 "기사 본문", PageType=news + article=false 가 "뉴스 인덱스").
	Article bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// FieldConfidence 는 단일 필드의 추출 신뢰도.
//
//   - HitRate     : 0.0~1.0 — sample 중 selector 가 매칭 + 유효 결과를 산출한 비율
//   - SampleCount : 분모 — 신뢰도 계산에 사용된 sample 수 (단일 sample 환경은 1)
//
// published_at 등 형식 검증이 필요한 필드는 추가로 형식 검증 (예: time.Parse) 통과해야 hit 로 계수.
type FieldConfidence struct {
	HitRate     float64 `json:"hit_rate"`
	SampleCount int     `json:"sample_count"`
}

// ParserRuleFilter 는 List 조회 시 필터 조건입니다.
type ParserRuleFilter struct {
	SourceName  string     // 빈 문자열이면 전체
	HostPattern string     // 빈 문자열이면 전체
	TargetType  TargetType // 빈 문자열이면 전체
	OnlyEnabled bool       // true 면 enabled=true 만

	// Article: nil 이면 미적용 (article 무관 전체).
	// 비-nil 이면 *Article 값과 동일한 행만 (true → article=TRUE, false → article=FALSE).
	// tri-state 필요 — bool zero-value 가 "미적용" 인지 "false 매칭" 인지 구분되어야 함 (이슈 #421).
	Article *bool

	Limit  int // 0 이면 기본값 (50)
	Offset int
}

// ParserRuleRepository 는 parser_rules 테이블에 대한 데이터 접근 인터페이스입니다.
//
// ParserRuleRepository is the data access interface for parser_rules.
// All implementations must be goroutine-safe.
type ParserRuleRepository interface {
	// Insert 는 새 규칙을 저장합니다. 자연키 충돌 시 ErrDuplicate 반환.
	// 성공 시 r.ID 가 채워집니다.
	Insert(ctx context.Context, r *ParserRuleRecord) error

	// Update 는 ID 로 규칙을 갱신합니다. 존재하지 않으면 ErrNotFound 반환.
	// 갱신 가능 필드: Selectors, Confidence, Enabled, Description, Article (자연키 + PageType 은 변경 불가).
	Update(ctx context.Context, r *ParserRuleRecord) error

	// UpdatePathPattern 은 정밀화 워크플로 에서 호출 — path_pattern + description 갱신.
	//
	// pattern 이 비어있지 않으면 RE2 컴파일 검증 (Insert 와 동일 정책) — 실패 시 ErrInvalid.
	//
	// optimistic guard: 대상 rule 이 여전히
	// (source_name='llm-auto' AND enabled=TRUE AND path_pattern='') 상태일 때만 적용.
	// 가드 실패 또는 rule 미존재 시 ErrNotFound — 호출자가 lost-update 회피용으로 분기.
	//
	// description 은 정밀화 시각 / 방식 (algorithm / llm) 등 추적용 history append.
	UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error

	// GetByID 는 ID 로 규칙을 조회합니다.
	GetByID(ctx context.Context, id int64) (*ParserRuleRecord, error)

	// FindActive 는 host + target_type 에 매칭되는 활성 규칙을 반환합니다 (RuleResolver 핫패스).
	// 같은 (host, type) 에 여러 활성 row 가 있다면 version DESC 순으로 첫 항목 반환.
	// 매칭 없으면 ErrNotFound.
	//
	// Deprecated: path_pattern 도입 후 후보 슬라이스를 한꺼번에 받아 application 측에서
	// 매칭하는 FindActiveCandidates 사용 권장. 본 메소드는 후방 호환을 위해 유지 — 내부적으로
	// FindActiveCandidates 의 첫 항목 (length DESC 정렬, '' 포함) 을 반환합니다.
	FindActive(ctx context.Context, host string, targetType TargetType) (*ParserRuleRecord, error)

	// InsertNextVersion 은 (source_name, host_pattern, path_pattern, target_type) 자연키의 다음
	// version 으로 rec 을 INSERT 합니다.
	//
	// 사용처:
	//   - Stale rule 재학습: 기존 v1 (catch-all enabled=true) 잔존 + 신규 v2 (정밀 / 갱신 selector)
	//     → Resolver 가 LENGTH(path_pattern) DESC + version DESC 로 우선순위 결정
	//   - Refiner path_pattern 정밀화: catch-all (v1, path="") + 정밀 (v2, path="/news/.*") 공존
	//     → v2 미매칭 path 는 v1 catch-all 로 fallback (silent miss 회피)
	//
	// 동작:
	//   1. (source_name, host_pattern, path_pattern, target_type) 의 MAX(version) 조회
	//   2. 없으면 version=1 로 INSERT, 있으면 max+1 로 INSERT
	//   3. 자연키 충돌 (race window) 시 ErrDuplicate 반환 — 호출자 retry 또는 흡수
	//
	// rec.Version 은 입력 무관 (자동 계산). 성공 시 rec.ID / Version / CreatedAt / UpdatedAt 채워짐.
	InsertNextVersion(ctx context.Context, r *ParserRuleRecord) error

	// HasAnyRule 은 (host_pattern, target_type) 에 대한 룰 존재 여부를 반환합니다.
	//
	// FindActiveCandidates 와 달리 enabled 필터 없음 — disabled 룰도 \"존재함\" 으로 카운트.
	//
	// 반환:
	//   - exists       : 어느 row 라도 (enabled / disabled 무관) 있으면 true
	//   - hasEnabled   : exists=true 일 때 enabled=TRUE row 가 하나라도 있으면 true
	//   - err          : DB 조회 실패
	//
	// 용도: parser_worker 가 ErrNoRule 시 LLM 재학습 트리거 여부 결정 — disabled 룰만 잔존인
	// host 는 운영자 수동 재활성 영역으로 분류.
	HasAnyRule(ctx context.Context, hostPattern string, targetType TargetType) (exists, hasEnabled bool, err error)

	// FindByNaturalKey 는 자연키 (source_name, host_pattern, path_pattern, target_type, version)
	// 로 단일 rule 을 조회합니다. enabled 필터 없음 — disabled 룰도 반환.
	//
	// 용도: llmgen.Generator 가 Insert 전에 동일 자연키 룰의 존재 여부를 확인하여 LLM 호출을
	// 회피하는 사전 lookup. Insert 시 ErrDuplicate 위반과 동일한 자연키를 검사합니다.
	//
	// 매칭 없으면 ErrNotFound.
	FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, targetType TargetType, version int) (*ParserRuleRecord, error)

	// FindActiveCandidates 는 host + target_type 매칭 활성 rule 들을 LENGTH(path_pattern) DESC,
	// version DESC 정렬로 반환합니다.
	//
	// Resolver 가 반환된 슬라이스를 application 측에서 URL path 와 regex 매칭 — 첫 매칭 rule 채택.
	// path_pattern='' 인 row 는 길이 0 으로 가장 마지막에 위치 (catch-all).
	//
	// 매칭 없으면 빈 슬라이스 + nil 에러 (ErrNotFound 아님 — 호출자가 빈 슬라이스로 분기).
	FindActiveCandidates(ctx context.Context, host string, targetType TargetType) ([]*ParserRuleRecord, error)

	// List 는 필터 조건에 맞는 규칙들을 반환합니다 (운영 대시보드용).
	List(ctx context.Context, filter ParserRuleFilter) ([]*ParserRuleRecord, error)

	// Delete 는 ID 로 규칙을 삭제합니다. 존재하지 않아도 nil 반환 (idempotent).
	Delete(ctx context.Context, id int64) error
}
