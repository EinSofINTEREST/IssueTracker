// Package grpc provides a gRPC client for the ELArchive Classifier service.
//
// grpc 패키지는 ELArchive Classifier gRPC 서버에 연결하는 클라이언트를 제공합니다.
//
// Classifier gRPC 서비스는 기본 포트 50051에서 실행됩니다.
// proto 정의: proto/classifier/classifier.proto
// 생성 코드: internal/classifier/grpc/pb/
package grpc

import (
  "context"
  "fmt"
  "time"

  "google.golang.org/grpc"
  "google.golang.org/grpc/credentials/insecure"

  "issuetracker/internal/classifier"
  pb "issuetracker/internal/classifier/grpc/pb"
)

const (
  defaultTarget  = "localhost:50051"
  defaultTimeout = 30 * time.Second
)

// Client는 Classifier gRPC 클라이언트입니다.
type Client struct {
  conn   *grpc.ClientConn
  stub   pb.ClassifierServiceClient
  timeout time.Duration
}

// Config는 gRPC 클라이언트 설정입니다.
type Config struct {
  Target  string        // gRPC 서버 주소 (예: "localhost:50051")
  Timeout time.Duration // 요청별 타임아웃
}

// DefaultConfig는 로컬 개발 환경 기본 설정을 반환합니다.
func DefaultConfig() Config {
  return Config{
    Target:  defaultTarget,
    Timeout: defaultTimeout,
  }
}

// NewClient는 새로운 Classifier gRPC 클라이언트를 생성합니다.
// 실제 연결은 첫 RPC 호출 시 수립됩니다 (lazy dial).
func NewClient(cfg Config) (*Client, error) {
  if cfg.Target == "" {
    cfg.Target = defaultTarget
  }
  if cfg.Timeout == 0 {
    cfg.Timeout = defaultTimeout
  }

  // 개발 환경: insecure 연결 (프로덕션에서는 TLS credentials로 교체 필요)
  conn, err := grpc.NewClient(
    cfg.Target,
    grpc.WithTransportCredentials(insecure.NewCredentials()),
  )
  if err != nil {
    return nil, fmt.Errorf("create grpc client: %w", err)
  }

  return &Client{
    conn:    conn,
    stub:    pb.NewClassifierServiceClient(conn),
    timeout: cfg.Timeout,
  }, nil
}

// Classify는 텍스트 1건을 분류합니다.
func (c *Client) Classify(ctx context.Context, text string, categories []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
  ctx, cancel := context.WithTimeout(ctx, c.timeout)
  defer cancel()

  resp, err := c.stub.Classify(ctx, &pb.ClassifyRequest{
    Text:       text,
    Categories: toPBCategories(categories),
  })
  if err != nil {
    return nil, fmt.Errorf("grpc classify: %w", err)
  }

  return &classifier.ClassifyResponse{
    Result: fromPBResult(resp.GetResult()),
  }, nil
}

// ClassifyBatch는 텍스트 여러 건을 분류합니다 (최대 100건).
func (c *Client) ClassifyBatch(ctx context.Context, texts []string, categories []classifier.CategoryInput) (*classifier.BatchClassifyResponse, error) {
  ctx, cancel := context.WithTimeout(ctx, c.timeout)
  defer cancel()

  resp, err := c.stub.ClassifyBatch(ctx, &pb.BatchClassifyRequest{
    Texts:      texts,
    Categories: toPBCategories(categories),
  })
  if err != nil {
    return nil, fmt.Errorf("grpc classify batch: %w", err)
  }

  items := make([]classifier.BatchClassifyItem, len(resp.GetResults()))
  for i, r := range resp.GetResults() {
    items[i] = classifier.BatchClassifyItem{
      Index:       int(r.GetIndex()),
      TextPreview: r.GetTextPreview(),
      Result:      fromPBResult(r.GetResult()),
    }
  }

  return &classifier.BatchClassifyResponse{
    Total:   int(resp.GetTotal()),
    Results: items,
  }, nil
}

// Health는 Classifier 서비스 상태를 확인합니다.
func (c *Client) Health(ctx context.Context) (*classifier.HealthResponse, error) {
  ctx, cancel := context.WithTimeout(ctx, c.timeout)
  defer cancel()

  resp, err := c.stub.Health(ctx, &pb.HealthRequest{})
  if err != nil {
    return nil, fmt.Errorf("grpc health: %w", err)
  }

  return &classifier.HealthResponse{
    Status:               resp.GetStatus(),
    ModelLoaded:          resp.GetModelLoaded(),
    ModelPath:            resp.GetModelPath(),
    DefaultCategoryCount: int(resp.GetDefaultCategoryCount()),
  }, nil
}

// Close는 gRPC 연결을 닫습니다.
func (c *Client) Close() error {
  return c.conn.Close()
}

// ─────────────────────────────────────────────────────────────────────────────
// 내부 변환 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func toPBCategories(categories []classifier.CategoryInput) []*pb.CategoryInput {
  if categories == nil {
    return nil
  }
  out := make([]*pb.CategoryInput, len(categories))
  for i, c := range categories {
    out[i] = &pb.CategoryInput{
      Name:        c.Name,
      Description: c.Description,
    }
  }
  return out
}

func fromPBResult(r *pb.ClassifyResult) classifier.ClassifyResult {
  if r == nil {
    return classifier.ClassifyResult{}
  }
  return classifier.ClassifyResult{
    Label:     r.GetLabel(),
    Reason:    r.GetReason(),
    ParseOk:   r.GetParseOk(),
    RawOutput: r.GetRawOutput(),
  }
}
