package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// SourceType은 데이터 소스의 타입을 나타냅니다.
type SourceType string

const (
	SourceTypeNews      SourceType = "news"
	SourceTypeCommunity SourceType = "community"
	SourceTypeSocial    SourceType = "social"
)

// SourceInfo는 데이터 소스의 정보를 담고 있습니다.
type SourceInfo struct {
	Country  string     // ISO 3166-1 alpha-2 (US, KR)
	Type     SourceType // News, Community, Social
	Name     string
	BaseURL  string
	Language string // ISO 639-1 (en, ko)
}

// TargetType은 크롤링 대상의 타입을 나타냅니다.
type TargetType string

const (
	TargetTypeFeed     TargetType = "feed"
	TargetTypeSitemap  TargetType = "sitemap"
	TargetTypeArticle  TargetType = "article"
	TargetTypeCategory TargetType = "category"
)

// Target은 크롤링 대상을 나타냅니다.
type Target struct {
	URL      string
	Type     TargetType
	Metadata map[string]interface{}
}

// RawContent는 크롤링한 원본 데이터를 나타냅니다.
type RawContent struct {
	ID         string
	SourceInfo SourceInfo
	FetchedAt  time.Time
	URL        string
	HTML       string
	StatusCode int
	Headers    map[string]string
	Metadata   map[string]interface{}
}

// NewRawContent 는 fetcher 들이 공통으로 사용하는 RawContent 조립 패턴을
// 일원화한 생성자입니다 (이슈 #75 — fetcher 공통 추출).
//
// 인자:
//   - name       : crawler 이름 (ID prefix 로 사용)
//   - source     : SourceInfo (도메인/언어 등 메타데이터)
//   - target     : Target (URL/Metadata 추출원)
//   - html       : fetched HTML
//   - statusCode : HTTP 상태 코드
//   - headers    : HTTP 응답 헤더. nil 이면 빈 map 으로 보정.
//
// ID 형식: "<name>-<unix_nano>-<rand_hex>" — 시간순 정렬 가능 + 동시 호출 충돌 방지.
//   - <unix_nano>: 시간 정렬·디버깅 추적성
//   - <rand_hex>: 4바이트 (8자 hex) crypto/rand suffix
//                 → 동일 ns 에 발생한 여러 호출도 충돌 확률 사실상 0 (1/2^32 per ns)
//   - rand.Read 실패 시에도 시간 부분만으로 ID 가 생성되어 fetch 자체는 진행
//
// metadata 가공이 필요한 경우 (예: chromedp 의 partial_load 플래그) 호출자가
// target.Metadata 를 미리 가공하거나 반환된 RawContent.Metadata 를 덮어써서 처리.
// 본 생성자는 단순 대입만 수행 — 호출자별 변형 정책은 호출자가 책임.
func NewRawContent(
	name string,
	source SourceInfo,
	target Target,
	html string,
	statusCode int,
	headers map[string]string,
) *RawContent {
	if headers == nil {
		headers = make(map[string]string)
	}
	return &RawContent{
		ID:         newRawContentID(name),
		SourceInfo: source,
		FetchedAt:  time.Now(),
		URL:        target.URL,
		HTML:       html,
		StatusCode: statusCode,
		Headers:    headers,
		Metadata:   target.Metadata,
	}
}

// newRawContentID 는 "<name>-<unix_nano>-<rand_hex>" 형식의 RawContent ID 를 생성합니다.
// crypto/rand 4바이트 suffix 로 동시 호출 충돌을 방지합니다 (1/2^32 per ns 충돌 확률).
//
// rand.Read 실패는 무시하고 0-suffix 로 fallback — fetch 자체를 멈추지 않으며,
// 시간 부분만으로도 단일 fetcher 내 nano 정밀도 충돌은 매우 드뭄.
func newRawContentID(name string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%d-%s", name, time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

// RawContentRef는 Kafka raw 토픽에 발행되는 경량 참조 메시지입니다.
// HTML 본문을 제외하고 다운스트림 소비자가 Postgres에서 전체 데이터를 조회할 수 있는
// 최소한의 필드만 포함합니다.
type RawContentRef struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"`
	FetchedAt  time.Time  `json:"fetched_at"`
	SourceInfo SourceInfo `json:"source_info"`
}

// ContentRef는 contents 테이블에 저장된 Content의 경량 참조입니다.
// Kafka normalized/validated 토픽에서 Full Content 대신 전달되어,
// 다운스트림 소비자가 ref.ID로 DB에서 전체 데이터를 조회합니다.
type ContentRef struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"`
	Country    string     `json:"country"`
	SourceInfo SourceInfo `json:"source_info"`
}

// Content는 파싱된 컨텐츠 데이터를 나타냅니다.
// 뉴스 기사, 커뮤니티 게시글, 소셜 미디어 포스트 등을 포함합니다.
// DB 저장 시 3개 테이블로 분리됩니다:
//   - contents: 핵심 메타데이터 (핫 경로)
//   - content_bodies: 본문 텍스트 (상세 조회 전용)
//   - content_meta: 확장 메타데이터 (파이프라인 업데이트)
type Content struct {
	// Identity
	ID         string     `json:"id"`
	SourceID   string     `json:"source_id"`
	SourceType SourceType `json:"source_type"` // "news" | "community" | "social"
	Country    string     `json:"country"`
	Language   string     `json:"language"`

	// Content (content_bodies 테이블)
	Title   string `json:"title"`
	Body    string `json:"body"` // 상세 조회 시에만 채워짐
	Summary string `json:"summary"`

	// Metadata
	Author      string     `json:"author"`
	PublishedAt time.Time  `json:"published_at"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	Category    string     `json:"category"`
	Tags        []string   `json:"tags"`

	// Technical
	URL          string   `json:"url"`
	CanonicalURL string   `json:"canonical_url"`
	ImageURLs    []string `json:"image_urls"` // content_meta 테이블

	// Quality
	ContentHash string  `json:"content_hash"`
	WordCount   int     `json:"word_count"`  // content_bodies 테이블
	Reliability float32 `json:"reliability"` // 신뢰도 0.0~1.0 (0.0: 미검증)

	// Extension (content_meta 테이블)
	Extra map[string]interface{} `json:"extra,omitempty"` // 유형별 메타데이터 (JSONB)

	CreatedAt time.Time `json:"created_at"`
}

// Config는 크롤러 설정을 나타냅니다.
type Config struct {
	// HTTP Client 설정
	Timeout         time.Duration
	MaxIdleConns    int
	MaxConnsPerHost int
	UserAgent       string

	// Rate Limiting
	RequestsPerHour int
	BurstSize       int
	DNSCacheTTL     time.Duration // DNS 해석 결과 캐시 TTL (기본: 5분)

	// Retry 설정
	MaxRetries   int
	RetryBackoff time.Duration

	// Source 설정
	SourceInfo SourceInfo
}

// DefaultConfig는 기본 크롤러 설정을 반환합니다.
func DefaultConfig() Config {
	return Config{
		Timeout:         30 * time.Second,
		MaxIdleConns:    100,
		MaxConnsPerHost: 10,
		UserAgent:       "IssueTracker/1.0 (+https://example.com/bot) Go-http-client",
		RequestsPerHour: 100,
		BurstSize:       10,
		DNSCacheTTL:     5 * time.Minute,
		MaxRetries:      3,
		RetryBackoff:    1 * time.Second,
	}
}
