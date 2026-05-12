// Package categories 는 크롤링 대상의 콘텐츠 카테고리 enum 과 priority tier 매핑을 제공합니다.
//
// 카테고리는 도메인 중립 — 사이트별 URL/section 매핑은 호출자 (scheduler entries 등) 가 담당.
// 본 패키지는 enum + tier 매핑만 단일화하여 resolver / scheduler / publisher 가 공유.
//
// Tier 매핑 (이슈 #381):
//   - High   : 시의성 + 사회적 영향 高 — 정치 / 경제 / 사회 / 시사 / 속보
//   - Normal : 일반 갱신 — 스포츠 / 문화 / IT / 연예 / 라이프 / 국제 / 커뮤니티
//   - Low    : 광범위 탐색 — 사이트맵 / 카테고리 hub / 백필 / 미분류
//
// 의존성 방향: 본 패키지는 self-contained — internal/ 패키지를 import 하지 않습니다.
// caller (internal/processor/fetcher/worker 등) 가 Tier 문자열을 core.Priority 로
// 매핑합니다 (CodeRabbit PR #384 피드백 — pkg/ → internal/ 역의존 차단).
package categories

// Category 는 콘텐츠의 도메인 분류입니다.
type Category string

// Category enum — scheduler entries 와 LLM extractor 의 page_type 양쪽에서 사용.
const (
	// High tier
	CategoryPolitics       Category = "politics"
	CategoryEconomy        Category = "economy"
	CategorySociety        Category = "society"
	CategoryCurrentAffairs Category = "current_affairs"
	CategoryBreakingNews   Category = "breaking_news"

	// Normal tier
	CategorySports        Category = "sports"
	CategoryCulture       Category = "culture"
	CategoryTech          Category = "tech"
	CategoryBusiness      Category = "business"
	CategoryEntertainment Category = "entertainment"
	CategoryLifestyle     Category = "lifestyle"
	CategoryInternational Category = "international"
	CategoryHealth        Category = "health"
	CategoryClimate       Category = "climate"
	CategoryColumn        Category = "column"
	CategoryCommunity     Category = "community"

	// Unknown — 미분류 (Low fallback)
	CategoryUnknown Category = ""
)

// MetadataKey 는 Target.Metadata 에 카테고리를 저장할 때 사용하는 표준 키입니다.
// publisher / resolver 가 동일 키로 read/write 하도록 단일화.
const MetadataKey = "category"

// highCategories 는 High tier 로 분류되는 카테고리 집합입니다.
var highCategories = map[Category]struct{}{
	CategoryPolitics:       {},
	CategoryEconomy:        {},
	CategorySociety:        {},
	CategoryCurrentAffairs: {},
	CategoryBreakingNews:   {},
}

// normalCategories 는 Normal tier 로 분류되는 카테고리 집합입니다.
var normalCategories = map[Category]struct{}{
	CategorySports:        {},
	CategoryCulture:       {},
	CategoryTech:          {},
	CategoryBusiness:      {},
	CategoryEntertainment: {},
	CategoryLifestyle:     {},
	CategoryInternational: {},
	CategoryHealth:        {},
	CategoryClimate:       {},
	CategoryColumn:        {},
	CategoryCommunity:     {},
}

// Tier 는 carrousel 우선순위 분류 문자열입니다 (high / normal / low).
//
// pkg/categories 가 self-contained 유지를 위해 core.Priority 대신 string 반환 — caller
// (internal/processor/fetcher/worker 의 resolver) 가 Tier → core.Priority 매핑.
type Tier string

const (
	TierHigh   Tier = "high"
	TierNormal Tier = "normal"
	TierLow    Tier = "low"
)

// Tier 는 본 카테고리가 매핑되는 우선순위 tier 를 반환합니다.
//
// 매핑되지 않은 카테고리 (CategoryUnknown 포함) 는 TierLow 반환 — cold-start / 미분류 처리.
func (c Category) Tier() Tier {
	if _, ok := highCategories[c]; ok {
		return TierHigh
	}
	if _, ok := normalCategories[c]; ok {
		return TierNormal
	}
	return TierLow
}

// IsKnown 은 본 카테고리가 enum 에 등록되어 있는지 반환합니다.
// 운영 / 디버깅 — 미등록 카테고리 hint 를 발견 시 로깅 / metric 용도.
func (c Category) IsKnown() bool {
	if c == CategoryUnknown {
		return false
	}
	if _, ok := highCategories[c]; ok {
		return true
	}
	if _, ok := normalCategories[c]; ok {
		return true
	}
	return false
}
