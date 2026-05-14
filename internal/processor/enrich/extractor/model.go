// 본 파일은 enrichment 단계의 도메인 schema 를 정의합니다 (이슈 #447).
//
// EnrichedFacts 는 page 에서 추출한 구조화된 facts (entities / claims / numeric facts /
// topics / sentiment) 를 담습니다. 후속 sub-issue (#448 / #449 / #450) 가 verifications /
// context / trust_score 필드를 추가합니다.
//
// 본 타입을 extractor 패키지에 배치한 이유: extractor 가 본 타입의 producer 이며,
// enrich/worker 도 extractor 만 import 하면 facts 타입에 접근 가능. enrich 루트
// 패키지 (Stage wrapper) 가 worker 를 import 하므로 본 model 을 enrich 루트에 두면
// import cycle 발생. extractor 가 cycle-free leaf 위치.
package extractor

// EntityType 은 추출된 entity 의 분류입니다.
type EntityType string

const (
	EntityTypePerson   EntityType = "person"
	EntityTypeOrg      EntityType = "org"
	EntityTypeLocation EntityType = "location"
	EntityTypeEvent    EntityType = "event"
	EntityTypeProduct  EntityType = "product"
)

// Sentiment 는 page 전체의 감정 톤입니다.
type Sentiment string

const (
	SentimentPositive Sentiment = "positive"
	SentimentNegative Sentiment = "negative"
	SentimentNeutral  Sentiment = "neutral"
)

// Entity 는 page 에서 추출된 named entity 입니다.
type Entity struct {
	Type     EntityType `json:"type"`
	Name     string     `json:"name"`
	Mentions int        `json:"mentions"`
}

// Claim 은 page 가 단언하는 한 가지 선언적 진술입니다.
// subject-predicate-object 분해는 후속 단계 (#448 교차 검증) 에서 활용됩니다.
type Claim struct {
	Text      string `json:"text"`
	Subject   string `json:"subject,omitempty"`
	Predicate string `json:"predicate,omitempty"`
	Object    string `json:"object,omitempty"`
}

// Fact 는 page 의 수치·날짜·정량적 사실입니다.
type Fact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

// EnrichedFacts 는 enrichment 단계의 종합 결과입니다.
//
// 본 sub-issue (#447) 는 extraction 결과만 채웁니다. Verifications / Context / TrustScore
// 필드는 schema 만 reserve — 후속 sub-issue 가 점진적으로 채웁니다.
type EnrichedFacts struct {
	Entities  []Entity  `json:"entities"`
	Claims    []Claim   `json:"claims"`
	Facts     []Fact    `json:"facts"`
	Topics    []string  `json:"topics"`
	Sentiment Sentiment `json:"sentiment"`

	// Verifications / Context / TrustScore: 후속 sub-issue 에서 채움 (#448 / #449 / #450).
	// 본 sub-issue 에서는 nil/empty 로 출발.
	Verifications []Verification `json:"verifications,omitempty"`
	Context       *PageContext   `json:"context,omitempty"`
	TrustScore    *float64       `json:"trust_score,omitempty"`
}

// Verification 은 claim 별 외부 소스 대조 결과입니다 (#448 에서 사용).
type Verification struct {
	ClaimIdx int      `json:"claim_idx"`
	Verdict  string   `json:"verdict"` // "supported" | "contradicted" | "unverified"
	Sources  []string `json:"sources,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// PageContext 는 외부 맥락 (사회·정치·공학 배경) 정보입니다 (#449 에서 사용).
type PageContext struct {
	Background   []BackgroundItem `json:"background,omitempty"`
	Timeline     []TimelineEvent  `json:"timeline,omitempty"`
	Implications *Implications    `json:"implications,omitempty"`
}

// BackgroundItem 은 entity / topic 별 배경 요약입니다.
type BackgroundItem struct {
	Subject  string   `json:"subject"`
	Category string   `json:"category"` // person | event | tech | policy
	Summary  string   `json:"summary"`
	Sources  []string `json:"sources,omitempty"`
}

// TimelineEvent 는 사건 타임라인 항목입니다.
type TimelineEvent struct {
	Date   string `json:"date"` // ISO 8601
	Event  string `json:"event"`
	Source string `json:"source,omitempty"`
}

// Implications 은 사회·정치·공학적 함의 요약입니다.
type Implications struct {
	Political string `json:"political,omitempty"`
	Social    string `json:"social,omitempty"`
	Technical string `json:"technical,omitempty"`
}
