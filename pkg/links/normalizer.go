package links

import (
	"fmt"
	"net/url"
	"strings"
)

// Normalizer는 URL 정규화를 수행합니다.
// 추적 파라미터 제거, 프로토콜 정규화, trailing slash 제거 등을 통해
// 동일 컨텐츠를 가리키는 URL이 항상 동일한 정규형을 갖도록 보장합니다.
//
// 사용 시점:
//   - 중복 탐지 (URLCache, DB content unique constraint)
//   - Kafka 파티션 키 일관성 (동일 기사가 동일 파티션으로 라우팅)
//   - CanonicalURL 표준화 (다운스트림 비교)
//
// 기본 동작은 보수적이며 fetch 가능성을 깨지 않도록 설계되었습니다.
// 자세한 옵션은 NewNormalizer 주석 참조.
type Normalizer struct {
	config normalizeConfig
}

// normalizeConfig는 Normalizer의 내부 설정을 보관합니다.
// 외부에서 직접 변경할 수 없으며 NormalizeOption을 통해서만 구성됩니다.
type normalizeConfig struct {
	// hostAllowedParams는 호스트(소문자, www. 제거)별로
	// 보존할 query 파라미터 키 집합을 매핑합니다.
	// 비어있거나 호스트가 없으면 모든 query 파라미터를 제거합니다.
	hostAllowedParams map[string]map[string]struct{}

	forceHTTPS         bool
	stripWWW           bool
	stripTrailingSlash bool
	stripFragment      bool
}

// NormalizeOption은 Normalizer의 동작을 변경하는 함수형 옵션입니다.
type NormalizeOption func(*normalizeConfig)

// WithAllowedParams는 특정 호스트에 대해 보존할 query 파라미터 이름을 등록합니다.
// 호스트 매칭은 case-insensitive 이며 "www." 접두사는 자동으로 정규화됩니다.
// 파라미터 키 매칭도 case-insensitive — 등록 시·필터링 시 모두 소문자로 정규화하여
// `Article_ID` / `article_id` / `ARTICLE_ID` 가 동일하게 처리됩니다.
//
// 예: 네이버 뉴스의 article_id, office_id는 컨텐츠 경로 분기에 필수이므로 보존:
//
//	WithAllowedParams("news.naver.com", "article_id", "office_id")
//
// 동일 호스트에 대해 여러 번 호출하면 파라미터 목록이 누적됩니다.
func WithAllowedParams(host string, params ...string) NormalizeOption {
	return func(c *normalizeConfig) {
		if c.hostAllowedParams == nil {
			c.hostAllowedParams = make(map[string]map[string]struct{})
		}
		key := normalizeHostKey(host)
		set, ok := c.hostAllowedParams[key]
		if !ok {
			set = make(map[string]struct{}, len(params))
			c.hostAllowedParams[key] = set
		}
		for _, p := range params {
			set[normalizeParamKey(p)] = struct{}{}
		}
	}
}

// WithKeepHTTP는 http → https 강제 변환을 비활성화합니다.
// http 전용 사이트(드물지만 존재)를 다룰 때 사용합니다.
func WithKeepHTTP() NormalizeOption {
	return func(c *normalizeConfig) { c.forceHTTPS = false }
}

// WithStripWWW는 호스트의 "www." 접두사 제거를 활성화합니다.
// 기본값(off): 일부 호스트는 www 없이는 미해결되므로 fetch 안전을 위해 보수적.
// 중복 탐지 강화 목적으로 활성화 가능.
func WithStripWWW() NormalizeOption {
	return func(c *normalizeConfig) { c.stripWWW = true }
}

// WithKeepTrailingSlash는 경로 끝 "/" 제거를 비활성화합니다.
// 기본값(on): 안전. trailing slash로 컨텐츠가 달라지는 사이트에서 사용.
func WithKeepTrailingSlash() NormalizeOption {
	return func(c *normalizeConfig) { c.stripTrailingSlash = false }
}

// WithKeepFragment는 fragment("#...") 제거를 비활성화합니다.
// 기본값(on): fragment는 클라이언트 사이드 처리로 서버 컨텐츠와 무관 — 보통 안전.
func WithKeepFragment() NormalizeOption {
	return func(c *normalizeConfig) { c.stripFragment = false }
}

// NewNormalizer는 새로운 Normalizer 인스턴스를 생성합니다.
//
// 기본 동작:
//   - 모든 query 파라미터 제거 (per-host 화이트리스트 비어있음)
//   - http → https 강제 변환 (forceHTTPS=true)
//   - "www." 접두사 유지 (stripWWW=false, fetch 안전 우선)
//   - 경로 끝 trailing slash 제거 (단, 루트 "/"는 유지)
//   - fragment("#...") 제거
//
// 호출자는 WithAllowedParams 등으로 사이트별 예외를 등록할 수 있습니다.
func NewNormalizer(opts ...NormalizeOption) *Normalizer {
	cfg := normalizeConfig{
		forceHTTPS:         true,
		stripWWW:           false,
		stripTrailingSlash: true,
		stripFragment:      true,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Normalizer{config: cfg}
}

// Normalize는 URL을 정규화하여 반환합니다.
//
// 동작:
//   - 빈 문자열 입력 → 빈 문자열 반환 (에러 없음)
//   - 파싱 실패 → 에러 반환
//   - host가 없는 상대 URL → 변경 없이 원본 반환 (정규화 대상 아님)
//
// 동일 컨텐츠를 가리키는 두 URL은 항상 동일한 정규형을 가져야 합니다.
func (n *Normalizer) Normalize(rawURL string) (string, error) {
	if rawURL == "" {
		return "", nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", rawURL, err)
	}

	// 절대 URL이 아니면 정규화 대상이 아님 (host 없음)
	if u.Host == "" {
		return rawURL, nil
	}

	// scheme 정규화
	if n.config.forceHTTPS && u.Scheme == "http" {
		u.Scheme = "https"
	}

	// host 정규화: 소문자화 + 옵션에 따른 www. 제거
	// u.Host 는 포트를 포함하므로 (예: "Example.com:8080"),
	// 호스트만 소문자화하고 포트는 보존하여 fetch 가능성을 깨지 않습니다.
	hostname := strings.ToLower(u.Hostname())
	if n.config.stripWWW {
		hostname = strings.TrimPrefix(hostname, "www.")
	}
	if port := u.Port(); port != "" {
		u.Host = hostname + ":" + port
	} else {
		u.Host = hostname
	}

	// query 정규화: 화이트리스트에 없는 파라미터 제거 + 키 정렬
	// 화이트리스트 매칭은 hostname (포트 제외) 기준 — 포트는 fetch 라우팅 정보일 뿐
	// 컨텐츠 정체성에는 영향 없음.
	if u.RawQuery != "" {
		u.RawQuery = n.filterQuery(hostname, u.RawQuery)
	}

	// trailing slash 제거 (루트 경로 "/"는 보존)
	if n.config.stripTrailingSlash && len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/")
	}

	// fragment 제거
	if n.config.stripFragment {
		u.Fragment = ""
		u.RawFragment = ""
	}

	return u.String(), nil
}

// filterQuery는 호스트별 화이트리스트에 따라 query 파라미터를 필터링합니다.
// url.Values.Encode 는 키 순으로 정렬된 결과를 반환하므로
// 동일 컨텐츠는 항상 동일한 query 정규형을 갖습니다.
//
// 키 매칭은 case-insensitive — `Article_ID`/`article_id` 가 동일하게 처리됩니다.
// ParseQuery 실패 시:
//   - 화이트리스트 호스트: 원본 rawQuery 보존 (필수 파라미터 손실 방지)
//   - 미등록 호스트: 빈 문자열 (전부 제거 정책 유지)
func (n *Normalizer) filterQuery(host, rawQuery string) string {
	allowed := n.allowedFor(host)
	if len(allowed) == 0 {
		// 화이트리스트 미등록 → 모든 파라미터 제거
		return ""
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		// 화이트리스트 호스트의 ParseQuery 실패 시 원본을 유지하여
		// 잘못된 파싱으로 인한 필수 파라미터 손실/fetch 실패를 방지합니다.
		return rawQuery
	}

	for k := range values {
		if _, ok := allowed[normalizeParamKey(k)]; !ok {
			values.Del(k)
		}
	}

	if len(values) == 0 {
		return ""
	}
	return values.Encode()
}

// allowedFor는 호스트에 대한 허용 파라미터 집합을 반환합니다.
// 호스트는 normalizeHostKey로 정규화하여 매칭합니다.
func (n *Normalizer) allowedFor(host string) map[string]struct{} {
	if len(n.config.hostAllowedParams) == 0 {
		return nil
	}
	return n.config.hostAllowedParams[normalizeHostKey(host)]
}

// normalizeHostKey는 호스트를 화이트리스트 매칭용 키로 정규화합니다.
// 소문자화 + "www." 접두사 제거를 적용하여 등록자/검색자가
// 동일 매칭을 얻도록 보장합니다.
func normalizeHostKey(host string) string {
	h := strings.ToLower(host)
	return strings.TrimPrefix(h, "www.")
}

// normalizeParamKey는 query 파라미터 키를 화이트리스트 매칭용으로 정규화합니다.
// 단순 소문자화 — 등록·필터링 양쪽이 동일 규칙을 따라야 일관된 매칭이 보장됩니다.
func normalizeParamKey(key string) string {
	return strings.ToLower(key)
}

// defaultNormalizer는 NormalizeURL 패키지 헬퍼가 사용하는 기본 인스턴스입니다.
// 화이트리스트가 비어있으므로 모든 query 파라미터가 제거됩니다.
var defaultNormalizer = NewNormalizer()

// NormalizeURL은 기본 옵션으로 URL을 정규화하는 패키지 레벨 헬퍼입니다.
// 동일 옵션 조합으로 다회 호출 시에는 NewNormalizer 후 Normalize 호출이
// 알로케이션 측면에서 효율적입니다.
func NormalizeURL(rawURL string) (string, error) {
	return defaultNormalizer.Normalize(rawURL)
}
