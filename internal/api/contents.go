package api

import (
	"errors"
	"net/http"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// contentSummary 는 목록 응답의 항목입니다.
//
// core.Content 를 그대로 노출하지 않는 이유:
//   - Body 는 content_bodies 테이블에 있어 목록 조회 시 채워지지 않습니다. 그대로 쓰면
//     항상 "body":"" 가 실려 본문이 비어 있는 것처럼 보입니다.
//   - ContentHash / Extra / Article 은 파이프라인 내부 관심사라 외부 계약에 들이지 않습니다.
type contentSummary struct {
	ID           string     `json:"id"`
	SourceID     string     `json:"source_id"`
	SourceType   string     `json:"source_type"`
	Country      string     `json:"country"`
	Language     string     `json:"language"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Author       string     `json:"author"`
	PublishedAt  *time.Time `json:"published_at"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	Category     string     `json:"category"`
	Tags         []string   `json:"tags"`
	URL          string     `json:"url"`
	CanonicalURL string     `json:"canonical_url,omitempty"`
	ImageURLs    []string   `json:"image_urls"`
	WordCount    int        `json:"word_count"`
	Reliability  float32    `json:"reliability"`
	CreatedAt    time.Time  `json:"created_at"`
}

// contentDetail 은 상세 응답입니다 — 목록 필드에 본문을 더합니다.
type contentDetail struct {
	contentSummary
	Body string `json:"body"`
}

// contentListResponse 는 목록 응답의 최상위 형태입니다.
//
// Total 은 with_total=true 일 때만 채워집니다 — 무필터 Count 는 full scan 이므로
// 비용이 드는 경로를 호출자가 명시적으로 요청할 때만 수행합니다.
type contentListResponse struct {
	Items  []contentSummary `json:"items"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
	Total  *int64           `json:"total,omitempty"`
}

// toSummary 는 core.Content 를 외부 계약 형태로 변환합니다.
func toSummary(c *core.Content) contentSummary {
	s := contentSummary{
		ID:           c.ID,
		SourceID:     c.SourceID,
		SourceType:   string(c.SourceType),
		Country:      c.Country,
		Language:     c.Language,
		Title:        c.Title,
		Summary:      c.Summary,
		Author:       c.Author,
		UpdatedAt:    c.UpdatedAt,
		Category:     c.Category,
		Tags:         c.Tags,
		URL:          c.URL,
		CanonicalURL: c.CanonicalURL,
		ImageURLs:    c.ImageURLs,
		WordCount:    c.WordCount,
		Reliability:  c.Reliability,
		CreatedAt:    c.CreatedAt,
	}

	// zero-value PublishedAt 은 "미상" 이다 (core.Content 주석). 0001-01-01 을 그대로 싣지 않고
	// null 로 내보내 호출자가 "미상" 과 "아주 오래된 기사" 를 구분할 수 있게 한다.
	if !c.PublishedAt.IsZero() {
		published := c.PublishedAt
		s.PublishedAt = &published
	}

	// nil 슬라이스는 JSON null 이 되어 호출자가 매번 null 검사를 해야 한다 — 빈 배열로 통일.
	if s.Tags == nil {
		s.Tags = []string{}
	}
	if s.ImageURLs == nil {
		s.ImageURLs = []string{}
	}

	return s
}

// writeRepoError 는 repository 에러를 상태 코드로 분류해 기록합니다.
//
// query timeout (이슈 #427 의 QueryTimeout decorator) 을 500 이 아닌 504 로 내보냅니다 —
// 호출자에게는 재시도 가치가 있다는 신호이고, 운영자에게는 코드 결함이 아니라 DB 부하라는 신호입니다.
func writeRepoError(w http.ResponseWriter, log *logger.Logger, message string, err error) {
	if storage.IsQueryTimeout(err) {
		WriteError(w, log, http.StatusGatewayTimeout, CodeTimeout, "storage timeout", err)
		return
	}

	WriteError(w, log, http.StatusInternalServerError, CodeInternal, message, err)
}

// NewContentListHandler 는 GET /api/contents 핸들러를 반환합니다.
func NewContentListHandler(repo repository.ContentRepository, log *logger.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if repo == nil {
			WriteError(w, log, http.StatusInternalServerError, CodeInternal, "content repository unavailable", nil)
			return
		}

		q, err := parseContentQuery(r.URL.Query())
		if err != nil {
			var pe *paramError
			if errors.As(err, &pe) {
				WriteError(w, log, http.StatusBadRequest, CodeBadRequest, pe.message, nil)
				return
			}
			WriteError(w, log, http.StatusInternalServerError, CodeInternal, "internal error", err)
			return
		}

		contents, err := repo.List(r.Context(), q.filter)
		if err != nil {
			writeRepoError(w, log, "failed to list contents", err)
			return
		}

		items := make([]contentSummary, 0, len(contents))
		for _, c := range contents {
			items = append(items, toSummary(c))
		}

		resp := contentListResponse{
			Items:  items,
			Limit:  q.filter.Pagination.Limit,
			Offset: q.filter.Pagination.Offset,
		}

		if q.withTotal {
			total, cerr := repo.Count(r.Context(), q.filter)
			if cerr != nil {
				writeRepoError(w, log, "failed to count contents", cerr)
				return
			}
			resp.Total = &total
		}

		WriteJSON(w, log, http.StatusOK, resp)
	}
}

// NewContentDetailHandler 는 GET /api/contents/{id} 핸들러를 반환합니다.
func NewContentDetailHandler(repo repository.ContentRepository, log *logger.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if repo == nil {
			WriteError(w, log, http.StatusInternalServerError, CodeInternal, "content repository unavailable", nil)
			return
		}

		id := r.PathValue("id")
		if id == "" {
			WriteError(w, log, http.StatusBadRequest, CodeBadRequest, "id must not be empty", nil)
			return
		}

		content, err := repo.GetByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				WriteError(w, log, http.StatusNotFound, CodeNotFound, "content not found", nil)
				return
			}
			writeRepoError(w, log, "failed to fetch content", err)
			return
		}

		WriteJSON(w, log, http.StatusOK, contentDetail{
			contentSummary: toSummary(content),
			Body:           content.Body,
		})
	}
}
