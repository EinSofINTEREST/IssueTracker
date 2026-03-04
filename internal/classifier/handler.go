package classifier

import (
	"context"
	"fmt"

	"issuetracker/pkg/logger"
)

// Protocol은 Classifier 연결 프로토콜을 나타냅니다.
type Protocol int

const (
	// ProtocolGRPC는 gRPC 프로토콜을 사용합니다 (기본값, 낮은 지연).
	ProtocolGRPC Protocol = iota
	// ProtocolHTTP는 HTTP 프로토콜을 사용합니다.
	ProtocolHTTP
)

// Handler는 HTTP와 gRPC 클라이언트를 통합 관리합니다.
// 기본적으로 gRPC를 우선 사용하고, 실패 시 HTTP로 자동 전환(fallback)합니다.
//
// Handler manages both HTTP and gRPC clients for the Classifier service.
// It uses gRPC as the primary protocol and falls back to HTTP on failure.
type Handler struct {
	grpc     Classifier
	http     Classifier
	primary  Protocol
	fallback bool // true: 1차 실패 시 2차 프로토콜로 전환
	log      *logger.Logger
}

// HandlerConfig는 Handler 설정입니다.
type HandlerConfig struct {
	// Primary는 우선 사용할 프로토콜입니다 (기본: ProtocolGRPC).
	Primary Protocol
	// Fallback이 true이면 1차 프로토콜 실패 시 2차로 자동 전환합니다.
	Fallback bool
}

// DefaultHandlerConfig는 gRPC 우선, HTTP fallback 설정을 반환합니다.
func DefaultHandlerConfig() HandlerConfig {
	return HandlerConfig{
		Primary:  ProtocolGRPC,
		Fallback: true,
	}
}

// NewHandler는 새로운 통합 관리 Handler를 생성합니다.
// grpcClient 또는 httpClient 중 하나는 반드시 비어있지 않아야 합니다.
func NewHandler(grpcClient, httpClient Classifier, cfg HandlerConfig, log *logger.Logger) *Handler {
	return &Handler{
		grpc:     grpcClient,
		http:     httpClient,
		primary:  cfg.Primary,
		fallback: cfg.Fallback,
		log:      log,
	}
}

// Classify는 텍스트 1건을 분류합니다.
// 1차 프로토콜 실패 시 fallback이 활성화된 경우 2차로 재시도합니다.
func (h *Handler) Classify(ctx context.Context, text string, categories []CategoryInput) (*ClassifyResponse, error) {
	primary, secondary := h.clients()

	// primary/secondary 선택 후 primary가 nil일 수 있으므로 방어 로직 추가
	if primary == nil {
		if secondary != nil {
			// 사용 가능한 secondary를 primary로 승격
			primary, secondary = secondary, nil
		} else {
			return nil, fmt.Errorf("classify: no available classifier client")
		}
	}
	resp, err := primary.Classify(ctx, text, categories)
	if err == nil {
		return resp, nil
	}

	if !h.fallback || secondary == nil {
		return nil, fmt.Errorf("classify: %w", err)
	}

	h.log.WithError(err).Warn("primary classifier failed, falling back to secondary protocol")

	resp, fallbackErr := secondary.Classify(ctx, text, categories)
	if fallbackErr != nil {
		return nil, fmt.Errorf("classify (primary: %w, fallback: %w)", err, fallbackErr)
	}

	return resp, nil
}

// ClassifyBatch는 텍스트 여러 건을 분류합니다 (최대 100건).
func (h *Handler) ClassifyBatch(ctx context.Context, texts []string, categories []CategoryInput) (*BatchClassifyResponse, error) {
	primary, secondary := h.clients()

	resp, err := primary.ClassifyBatch(ctx, texts, categories)
	if err == nil {
		return resp, nil
	}

	if !h.fallback || secondary == nil {
		return nil, fmt.Errorf("classify batch: %w", err)
	}

	h.log.WithError(err).Warn("primary classifier batch failed, falling back to secondary protocol")

	resp, fallbackErr := secondary.ClassifyBatch(ctx, texts, categories)
	if fallbackErr != nil {
		return nil, fmt.Errorf("classify batch (primary: %w, fallback: %w)", err, fallbackErr)
	}

	return resp, nil
}

// Health는 Classifier 서비스 상태를 확인합니다.
// gRPC와 HTTP 순서로 모두 시도하여 첫 번째 성공한 결과를 반환합니다.
func (h *Handler) Health(ctx context.Context) (*HealthResponse, error) {
	primary, secondary := h.clients()

	resp, err := primary.Health(ctx)
	if err == nil {
		return resp, nil
	}

	if secondary == nil {
		return nil, fmt.Errorf("health: %w", err)
	}

	return secondary.Health(ctx)
}

// Close는 모든 클라이언트 리소스를 해제합니다.
func (h *Handler) Close() error {
	var grpcErr, httpErr error

	if h.grpc != nil {
		grpcErr = h.grpc.Close()
	}
	if h.http != nil {
		httpErr = h.http.Close()
	}

	if grpcErr != nil && httpErr != nil {
		return fmt.Errorf("close grpc: %w; close http: %w", grpcErr, httpErr)
	}
	if grpcErr != nil {
		return fmt.Errorf("close grpc: %w", grpcErr)
	}
	if httpErr != nil {
		return fmt.Errorf("close http: %w", httpErr)
	}

	return nil
}

// clients는 설정된 primary에 따라 우선/보조 클라이언트를 반환합니다.
func (h *Handler) clients() (primary, secondary Classifier) {
	if h.primary == ProtocolGRPC {
		return h.grpc, h.http
	}
	return h.http, h.grpc
}
