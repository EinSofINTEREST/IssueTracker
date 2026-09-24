package api

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage/model"
)

const (
	// maxLimit: 단일 요청이 가져갈 수 있는 최대 레코드 수.
	//
	// 초과 시 조용히 잘라내지 않고 400 으로 거절합니다 — silent clamp 는 호출자가
	// "전부 받았다" 고 오해하게 만들어 페이지네이션 누락을 감춥니다.
	maxLimit = 200

	// maxStringParam: 자유 문자열 파라미터 (category / source) 길이 상한.
	maxStringParam = 200

	// maxTags: tags 필터에 허용하는 최대 개수.
	maxTags = 10
)

var (
	countryPattern  = regexp.MustCompile(`^[A-Za-z]{2}$`)
	languagePattern = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// allowedContentParams: /api/contents 가 인식하는 쿼리 파라미터 전체.
//
// 목록에 없는 키는 400 으로 거절합니다. 오타 (`?contry=US`) 를 조용히 무시하면
// 필터가 걸리지 않은 전체 결과가 200 으로 돌아와 호출자가 필터가 먹은 줄 압니다.
var allowedContentParams = map[string]struct{}{
	"country":          {},
	"language":         {},
	"category":         {},
	"source":           {},
	"source_type":      {},
	"published_after":  {},
	"published_before": {},
	"tags":             {},
	"min_reliability":  {},
	"limit":            {},
	"offset":           {},
	"with_total":       {},
}

// paramError 는 클라이언트에게 그대로 노출해도 되는 파라미터 검증 실패입니다.
type paramError struct {
	message string
}

func (e *paramError) Error() string { return e.message }

func badParam(format string, args ...interface{}) error {
	return &paramError{message: fmt.Sprintf(format, args...)}
}

// contentQuery 는 검증을 마친 /api/contents 요청 파라미터입니다.
type contentQuery struct {
	filter    model.ContentFilter
	withTotal bool
}

// parseContentQuery 는 쿼리 파라미터를 검증하여 ContentFilter 로 변환합니다.
//
// 검증 실패는 전부 paramError — 호출자가 400 으로 매핑합니다. 잘못된 값을 그대로
// WHERE 에 넣으면 "빈 결과 200" 이 되어 오타와 "결과 없음" 이 구분되지 않습니다.
func parseContentQuery(values url.Values) (contentQuery, error) {
	if err := rejectUnknownParams(values); err != nil {
		return contentQuery{}, err
	}

	q := contentQuery{filter: model.ContentFilter{Pagination: model.DefaultPagination()}}

	country := values.Get("country")
	if country != "" {
		if !countryPattern.MatchString(country) {
			return contentQuery{}, badParam("country must be an ISO 3166-1 alpha-2 code, got %q", country)
		}
		q.filter.Country = strings.ToUpper(country)
	}

	language := values.Get("language")
	if language != "" {
		if !languagePattern.MatchString(language) {
			return contentQuery{}, badParam("language must be an ISO 639-1 code, got %q", language)
		}
		q.filter.Language = strings.ToLower(language)
	}

	var err error
	if q.filter.Category, err = boundedString(values, "category"); err != nil {
		return contentQuery{}, err
	}
	if q.filter.Source, err = boundedString(values, "source"); err != nil {
		return contentQuery{}, err
	}

	if st := values.Get("source_type"); st != "" {
		switch core.SourceType(st) {
		case core.SourceTypeNews, core.SourceTypeCommunity, core.SourceTypeSocial:
			q.filter.SourceType = st
		default:
			return contentQuery{}, badParam("source_type must be one of news/community/social, got %q", st)
		}
	}

	if q.filter.PublishedAfter, err = optionalTime(values, "published_after"); err != nil {
		return contentQuery{}, err
	}
	if q.filter.PublishedBefore, err = optionalTime(values, "published_before"); err != nil {
		return contentQuery{}, err
	}
	if q.filter.PublishedAfter != nil && q.filter.PublishedBefore != nil &&
		q.filter.PublishedAfter.After(*q.filter.PublishedBefore) {
		// 뒤집힌 범위는 항상 빈 결과라 오타를 감춘다.
		return contentQuery{}, badParam("published_after must not be later than published_before")
	}

	if q.filter.Tags, err = parseTags(values.Get("tags")); err != nil {
		return contentQuery{}, err
	}

	if q.filter.MinReliability, err = optionalReliability(values); err != nil {
		return contentQuery{}, err
	}

	if q.filter.Pagination, err = parsePagination(values); err != nil {
		return contentQuery{}, err
	}

	if q.withTotal, err = optionalBool(values, "with_total"); err != nil {
		return contentQuery{}, err
	}

	return q, nil
}

// rejectUnknownParams 는 인식하지 못하는 쿼리 키를 거절합니다.
func rejectUnknownParams(values url.Values) error {
	var unknown []string
	for key := range values {
		if _, ok := allowedContentParams[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}

	sort.Strings(unknown) // 응답을 결정적으로 유지
	return badParam("unknown query parameter(s): %s", strings.Join(unknown, ", "))
}

func boundedString(values url.Values, key string) (string, error) {
	v := values.Get(key)
	if len(v) > maxStringParam {
		return "", badParam("%s must be at most %d characters", key, maxStringParam)
	}
	return v, nil
}

func optionalTime(values url.Values, key string) (*time.Time, error) {
	raw := values.Get(key)
	if raw == "" {
		return nil, nil
	}

	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, badParam("%s must be an RFC3339 timestamp, got %q", key, raw)
	}
	return &t, nil
}

func parseTags(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}

	tags := make([]string, 0, maxTags)
	for _, part := range strings.Split(raw, ",") {
		tag := strings.TrimSpace(part)
		if tag == "" {
			return nil, badParam("tags must not contain empty entries")
		}
		tags = append(tags, tag)
	}

	if len(tags) > maxTags {
		return nil, badParam("tags must contain at most %d entries", maxTags)
	}
	return tags, nil
}

func optionalReliability(values url.Values) (*float32, error) {
	raw := values.Get("min_reliability")
	if raw == "" {
		return nil, nil
	}

	f, err := strconv.ParseFloat(raw, 32)
	if err != nil {
		return nil, badParam("min_reliability must be a number, got %q", raw)
	}
	if f < 0 || f > 1 {
		return nil, badParam("min_reliability must be between 0 and 1, got %s", raw)
	}

	v := float32(f)
	return &v, nil
}

func parsePagination(values url.Values) (model.Pagination, error) {
	p := model.DefaultPagination()

	if raw := values.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return p, badParam("limit must be an integer, got %q", raw)
		}
		if n < 1 {
			return p, badParam("limit must be at least 1, got %d", n)
		}
		if n > maxLimit {
			return p, badParam("limit must be at most %d, got %d", maxLimit, n)
		}
		p.Limit = n
	}

	if raw := values.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return p, badParam("offset must be an integer, got %q", raw)
		}
		if n < 0 {
			return p, badParam("offset must not be negative, got %d", n)
		}
		p.Offset = n
	}

	return p, nil
}

func optionalBool(values url.Values, key string) (bool, error) {
	raw := values.Get(key)
	if raw == "" {
		return false, nil
	}

	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, badParam("%s must be a boolean, got %q", key, raw)
	}
	return b, nil
}
