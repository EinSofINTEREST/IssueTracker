// Package http provides an HTTP client for the ELArchive Classifier service.
//
// http 패키지는 ELArchive Classifier FastAPI 서버의 HTTP 엔드포인트에
// 연결하는 클라이언트를 제공합니다.
//
// 엔드포인트:
//   - POST /classify        — 텍스트 단건 분류
//   - POST /classify/batch  — 텍스트 배치 분류 (최대 100건)
//   - GET  /health          — 서비스 상태 확인
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"issuetracker/internal/classifier"
)

const (
	defaultTimeout  = 30 * time.Second
	defaultBaseURL  = "http://localhost:8000"
	maxResponseSize = 1 << 20 // 1MB
)

// Client는 Classifier HTTP API 클라이언트입니다.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// Config는 HTTP 클라이언트 설정입니다.
type Config struct {
	BaseURL string
	Timeout time.Duration
}

// DefaultConfig는 로컬 개발 환경 기본 설정을 반환합니다.
// 환경변수 기반 설정은 pkg/config.LoadClassifier()를 사용하세요.
func DefaultConfig() Config {
	return Config{
		BaseURL: defaultBaseURL,
		Timeout: defaultTimeout,
	}
}

// NewClient는 새로운 Classifier HTTP 클라이언트를 생성합니다.
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Client{
		baseURL: cfg.BaseURL,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Classify는 텍스트 1건을 분류합니다.
func (c *Client) Classify(ctx context.Context, text string, categories []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
	req := classifyRequest{
		Text:       text,
		Categories: toRequestCategories(categories),
	}

	var resp classifyResponse
	if err := c.post(ctx, "/classify", req, &resp); err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}

	return &classifier.ClassifyResponse{
		Result: classifier.ClassifyResult{
			Label:     resp.Result.Label,
			Reason:    resp.Result.Reason,
			ParseOk:   resp.Result.ParseOk,
			RawOutput: resp.Result.RawOutput,
		},
	}, nil
}

// ClassifyBatch는 텍스트 여러 건을 분류합니다 (최대 100건).
func (c *Client) ClassifyBatch(ctx context.Context, texts []string, categories []classifier.CategoryInput) (*classifier.BatchClassifyResponse, error) {
	req := batchClassifyRequest{
		Texts:      texts,
		Categories: toRequestCategories(categories),
	}

	var resp batchClassifyResponse
	if err := c.post(ctx, "/classify/batch", req, &resp); err != nil {
		return nil, fmt.Errorf("classify batch: %w", err)
	}

	items := make([]classifier.BatchClassifyItem, len(resp.Results))
	for i, r := range resp.Results {
		items[i] = classifier.BatchClassifyItem{
			Index:       r.Index,
			TextPreview: r.TextPreview,
			Result: classifier.ClassifyResult{
				Label:     r.Result.Label,
				Reason:    r.Result.Reason,
				ParseOk:   r.Result.ParseOk,
				RawOutput: r.Result.RawOutput,
			},
		}
	}

	return &classifier.BatchClassifyResponse{
		Total:   resp.Total,
		Results: items,
	}, nil
}

// Health는 Classifier 서비스 상태를 확인합니다.
func (c *Client) Health(ctx context.Context) (*classifier.HealthResponse, error) {
	url := c.baseURL + "/health"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", httpResp.StatusCode, body)
	}

	var resp healthResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &classifier.HealthResponse{
		Status:               resp.Status,
		ModelLoaded:          resp.ModelLoaded,
		ModelPath:            resp.ModelPath,
		DefaultCategoryCount: resp.DefaultCategoryCount,
	}, nil
}

// Close는 HTTP 클라이언트 리소스를 해제합니다.
func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 내부 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, respBody)
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

func toRequestCategories(categories []classifier.CategoryInput) []categoryInput {
	if categories == nil {
		return nil
	}
	out := make([]categoryInput, len(categories))
	for i, c := range categories {
		out[i] = categoryInput{Name: c.Name, Description: c.Description}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP 요청/응답 DTO (Classifier FastAPI 스키마와 1:1 대응)
// ─────────────────────────────────────────────────────────────────────────────

type categoryInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type classifyRequest struct {
	Text       string          `json:"text"`
	Categories []categoryInput `json:"categories,omitempty"`
}

type classifyResultDTO struct {
	Label     string `json:"label"`
	Reason    string `json:"reason"`
	ParseOk   bool   `json:"parse_ok"`
	RawOutput string `json:"raw_output,omitempty"`
}

type classifyResponse struct {
	Result classifyResultDTO `json:"result"`
}

type batchClassifyRequest struct {
	Texts      []string        `json:"texts"`
	Categories []categoryInput `json:"categories,omitempty"`
}

type batchClassifyItemDTO struct {
	Index       int               `json:"index"`
	TextPreview string            `json:"text_preview"`
	Result      classifyResultDTO `json:"result"`
}

type batchClassifyResponse struct {
	Total   int                    `json:"total"`
	Results []batchClassifyItemDTO `json:"results"`
}

type healthResponse struct {
	Status               string `json:"status"`
	ModelLoaded          bool   `json:"model_loaded"`
	ModelPath            string `json:"model_path"`
	DefaultCategoryCount int    `json:"default_category_count"`
}
