// Package search 는 검색 엔진 기반 진입 (Google CSE 등) 의 fetcher domain handler 를 제공합니다.
//
// 일반 fetcher chain (goquery / chromedp) 과 달리 검색은 API 응답 → article URL 추출 → fanout
// 구조이므로 별도 handler 로 격리. 본 패키지는 SearchHandler 와 Google CSE client 를 포함합니다.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"issuetracker/pkg/logger"
)

// CSEDefaultBaseURL 은 Google Custom Search JSON API 의 기본 endpoint 입니다.
const CSEDefaultBaseURL = "https://customsearch.googleapis.com/customsearch/v1"

// cseMaxResultsPerCall 은 Google CSE 가 한 번의 호출에서 반환하는 최대 결과 수입니다.
// num 파라미터 상한 — paginated start 1,11,21,... 으로 다음 페이지 진입.
const cseMaxResultsPerCall = 10

// cseMaxStart 는 Google CSE 가 허용하는 start 파라미터 최댓값 (총 100개 결과 = 10페이지).
const cseMaxStart = 100

// CSEClient 는 Google Custom Search Engine 의 JSON API 를 호출하는 client 입니다.
//
// 동시성 안전 — internal 상태는 모두 readonly 또는 http client 자체에서 관리.
type CSEClient struct {
	apiKey  string
	cx      string
	baseURL string
	http    *http.Client
	log     *logger.Logger
}

// CSEClientOptions 는 CSEClient 생성 옵션입니다.
type CSEClientOptions struct {
	APIKey  string
	CX      string // Search Engine ID
	BaseURL string // 빈 문자열이면 CSEDefaultBaseURL
	Timeout time.Duration
}

// NewCSEClient 는 새 CSEClient 를 생성합니다.
//
// APIKey / CX 가 비어있으면 에러. timeout 이 0 이하면 10s 적용.
func NewCSEClient(opts CSEClientOptions, log *logger.Logger) (*CSEClient, error) {
	if opts.APIKey == "" {
		return nil, errors.New("search: CSEClient requires APIKey")
	}
	if opts.CX == "" {
		return nil, errors.New("search: CSEClient requires CX (search engine id)")
	}
	if log == nil {
		return nil, errors.New("search: CSEClient requires logger")
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = CSEDefaultBaseURL
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &CSEClient{
		apiKey:  opts.APIKey,
		cx:      opts.CX,
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
		log:     log,
	}, nil
}

// SearchOptions 는 단일 keyword 호출에 적용되는 파라미터입니다.
//
// MaxResults 는 본 client 가 paginated 호출로 모을 총 결과 수 — Google CSE 자체 상한
// (cseMaxStart=100) 을 넘으면 100 으로 cap.
type SearchOptions struct {
	MaxResults    int    // 누적 결과 상한 (10 단위로 paginate)
	DateRangeDays int    // 0 이면 dateRestrict 미적용
	Language      string // CSE 'lr' 파라미터 (예: "lang_ko" 형식이 아니라 ISO 코드 — 본 client 가 변환)
	Region        string // CSE 'gl' 파라미터 (소문자 ISO 코드)
}

// CSEError 는 CSE API 호출 실패를 나타냅니다.
//
// Retryable=true 인 경우 호출자가 backoff 후 재시도 — 429 / 5xx / network.
// 401/403 등 auth 에러는 Retryable=false (재시도 무용).
type CSEError struct {
	StatusCode int
	Message    string
	Retryable  bool
	Err        error
}

func (e *CSEError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("cse api error (status=%d): %s: %v", e.StatusCode, e.Message, e.Err)
	}
	return fmt.Sprintf("cse api error (status=%d): %s", e.StatusCode, e.Message)
}

func (e *CSEError) Unwrap() error { return e.Err }

// cseResponse 는 Google CSE JSON 응답의 부분 매핑 — 필요한 필드만 추출.
type cseResponse struct {
	Items []struct {
		Link string `json:"link"`
	} `json:"items"`
	Queries struct {
		NextPage []struct {
			StartIndex int `json:"startIndex"`
		} `json:"nextPage"`
	} `json:"queries"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Search 는 단일 keyword 로 paginated 호출하여 article URL 리스트를 반환합니다.
//
// MaxResults 가 0 이하면 cseMaxResultsPerCall (10) 적용. 100 초과 시 100 으로 cap.
// 호출 도중 retryable 에러 발생 시: 첫 page 실패는 그대로 반환, 중간 page 실패는 그때까지의
// 결과를 부분 반환 + warn (CSE 무료 plan quota 고려 — 부분 결과도 가치 있음).
func (c *CSEClient) Search(ctx context.Context, keyword string, opts SearchOptions) ([]string, error) {
	if keyword == "" {
		return nil, errors.New("search: keyword must be non-empty")
	}
	max := opts.MaxResults
	if max <= 0 {
		max = cseMaxResultsPerCall
	}
	if max > cseMaxStart {
		max = cseMaxStart
	}

	var urls []string
	seen := make(map[string]struct{}, max)

	for start := 1; start <= max && start <= cseMaxStart; start += cseMaxResultsPerCall {
		select {
		case <-ctx.Done():
			return urls, ctx.Err()
		default:
		}

		// 마지막 페이지에서는 num 을 남은 개수로 줄여 quota 절약.
		num := cseMaxResultsPerCall
		if remaining := max - (start - 1); remaining < num {
			num = remaining
		}

		page, hasNext, err := c.searchOnce(ctx, keyword, opts, start, num)
		if err != nil {
			// 부분 결과 보존: 첫 페이지가 아니면 warn + 누적 결과 반환.
			if start > 1 && len(urls) > 0 {
				c.log.WithFields(map[string]interface{}{
					"keyword":   keyword,
					"start":     start,
					"partial_n": len(urls),
				}).WithError(err).Warn("cse pagination interrupted — returning partial results")
				return urls, nil
			}
			return nil, err
		}

		for _, u := range page {
			if _, dup := seen[u]; dup {
				continue
			}
			seen[u] = struct{}{}
			urls = append(urls, u)
		}

		if !hasNext || len(urls) >= max {
			break
		}
	}

	return urls, nil
}

// searchOnce 는 1회의 CSE API 호출을 수행하고 (URLs, hasNextPage, err) 를 반환합니다.
func (c *CSEClient) searchOnce(ctx context.Context, keyword string, opts SearchOptions, start, num int) ([]string, bool, error) {
	q := url.Values{}
	q.Set("key", c.apiKey)
	q.Set("cx", c.cx)
	q.Set("q", keyword)
	q.Set("num", strconv.Itoa(num))
	q.Set("start", strconv.Itoa(start))
	if opts.DateRangeDays > 0 {
		q.Set("dateRestrict", fmt.Sprintf("d%d", opts.DateRangeDays))
	}
	if opts.Language != "" {
		// Google CSE 의 lr 파라미터 형식은 "lang_<ISO>" — 호출자는 ISO 코드만 전달.
		q.Set("lr", "lang_"+opts.Language)
	}
	if opts.Region != "" {
		q.Set("gl", opts.Region)
	}

	endpoint := c.baseURL + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, fmt.Errorf("build cse request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, &CSEError{Message: "transport error", Retryable: true, Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, &CSEError{StatusCode: resp.StatusCode, Message: "read response", Retryable: true, Err: err}
	}

	if resp.StatusCode >= 400 {
		// 4xx 분류: 429 / 408 은 retryable, 그 외 4xx (auth / quota exceeded etc) 는 non-retryable.
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500
		// 응답 body 의 error.message 가 있으면 추출.
		var parsed cseResponse
		_ = json.Unmarshal(body, &parsed)
		msg := ""
		if parsed.Error != nil {
			msg = parsed.Error.Message
		}
		if msg == "" {
			msg = string(body)
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return nil, false, &CSEError{StatusCode: resp.StatusCode, Message: msg, Retryable: retryable}
	}

	var parsed cseResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false, &CSEError{StatusCode: resp.StatusCode, Message: "decode json", Retryable: false, Err: err}
	}

	urls := make([]string, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		if it.Link != "" {
			urls = append(urls, it.Link)
		}
	}
	hasNext := len(parsed.Queries.NextPage) > 0
	return urls, hasNext, nil
}
