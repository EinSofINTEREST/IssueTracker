package classifier_test

import (
  "context"
  "errors"
  "io"
  "testing"

  "issuetracker/internal/classifier"
  "issuetracker/pkg/logger"

  "github.com/stretchr/testify/assert"
  "github.com/stretchr/testify/require"
)

// mockClassifier는 테스트에서 사용하는 Classifier 인터페이스 모의 구현체입니다.
type mockClassifier struct {
  classifyFn      func(ctx context.Context, text string, categories []classifier.CategoryInput) (*classifier.ClassifyResponse, error)
  classifyBatchFn func(ctx context.Context, texts []string, categories []classifier.CategoryInput) (*classifier.BatchClassifyResponse, error)
  healthFn        func(ctx context.Context) (*classifier.HealthResponse, error)
}

func (m *mockClassifier) Classify(ctx context.Context, text string, categories []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
  return m.classifyFn(ctx, text, categories)
}

func (m *mockClassifier) ClassifyBatch(ctx context.Context, texts []string, categories []classifier.CategoryInput) (*classifier.BatchClassifyResponse, error) {
  return m.classifyBatchFn(ctx, texts, categories)
}

func (m *mockClassifier) Health(ctx context.Context) (*classifier.HealthResponse, error) {
  if m.healthFn != nil {
    return m.healthFn(ctx)
  }
  return &classifier.HealthResponse{Status: "ok"}, nil
}

func (m *mockClassifier) Close() error { return nil }

// newTestLogger는 출력 없는 테스트용 로거를 생성합니다.
func newTestLogger() *logger.Logger {
  return logger.New(logger.Config{
    Level:  logger.LevelError,
    Output: io.Discard,
  })
}

// --- Classify: 기본 우선순위/폴백 ---

func TestHandler_Classify_Primary성공_Secondary미호출(t *testing.T) {
  // primary가 성공하면 secondary를 호출하지 않는지 검증합니다.
  want := &classifier.ClassifyResponse{Result: classifier.ClassifyResult{Label: "technology"}}

  primary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return want, nil
    },
  }
  secondaryCalled := false
  secondary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      secondaryCalled = true
      return nil, errors.New("secondary는 호출되면 안 됩니다")
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolGRPC, Fallback: true}
  h := classifier.NewHandler(primary, secondary, cfg, newTestLogger())

  got, err := h.Classify(context.Background(), "test", nil)
  require.NoError(t, err)
  assert.Equal(t, want, got)
  assert.False(t, secondaryCalled, "primary 성공 시 secondary를 호출하면 안 됩니다")
}

func TestHandler_Classify_Fallback활성화_Primary실패_Secondary성공(t *testing.T) {
  // primary 실패 + fallback=true 인 경우 secondary로 재시도하는지 검증합니다.
  primaryErr := errors.New("primary 연결 오류")
  want := &classifier.ClassifyResponse{Result: classifier.ClassifyResult{Label: "politics"}}

  primary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return nil, primaryErr
    },
  }
  secondary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return want, nil
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolGRPC, Fallback: true}
  h := classifier.NewHandler(primary, secondary, cfg, newTestLogger())

  got, err := h.Classify(context.Background(), "test", nil)
  require.NoError(t, err)
  assert.Equal(t, want, got)
}

func TestHandler_Classify_Fallback활성화_Primary실패_Secondary실패(t *testing.T) {
  // primary/secondary 모두 실패 시 두 에러를 모두 포함한 에러를 반환하는지 검증합니다.
  primaryErr := errors.New("primary 오류")
  secondaryErr := errors.New("secondary 오류")

  primary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return nil, primaryErr
    },
  }
  secondary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return nil, secondaryErr
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolGRPC, Fallback: true}
  h := classifier.NewHandler(primary, secondary, cfg, newTestLogger())

  _, err := h.Classify(context.Background(), "test", nil)
  require.Error(t, err)
  assert.ErrorIs(t, err, primaryErr)
  assert.ErrorIs(t, err, secondaryErr)
}

func TestHandler_Classify_Fallback비활성화_Primary실패(t *testing.T) {
  // fallback=false인 경우 primary 에러가 그대로 반환되고 secondary를 호출하지 않는지 검증합니다.
  primaryErr := errors.New("primary 오류")
  secondaryCalled := false

  primary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return nil, primaryErr
    },
  }
  secondary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      secondaryCalled = true
      return nil, errors.New("secondary는 호출되면 안 됩니다")
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolGRPC, Fallback: false}
  h := classifier.NewHandler(primary, secondary, cfg, newTestLogger())

  _, err := h.Classify(context.Background(), "test", nil)
  require.Error(t, err)
  assert.ErrorIs(t, err, primaryErr)
  assert.False(t, secondaryCalled, "fallback=false이면 secondary를 호출하면 안 됩니다")
}

// --- Classify: secondary가 nil인 경우 ---

func TestHandler_Classify_Secondary없음_Primary성공(t *testing.T) {
  // secondary가 nil이고 primary가 성공하는 경우 정상 동작하는지 검증합니다.
  want := &classifier.ClassifyResponse{Result: classifier.ClassifyResult{Label: "sports"}}

  primary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return want, nil
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolGRPC, Fallback: false}
  h := classifier.NewHandler(primary, nil, cfg, newTestLogger())

  got, err := h.Classify(context.Background(), "test", nil)
  require.NoError(t, err)
  assert.Equal(t, want, got)
}

func TestHandler_Classify_Secondary없음_Fallback활성화_Primary실패(t *testing.T) {
  // secondary가 nil이고 fallback=true인 경우에도 에러를 반환하는지 검증합니다.
  primaryErr := errors.New("primary 오류")

  primary := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return nil, primaryErr
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolGRPC, Fallback: true}
  h := classifier.NewHandler(primary, nil, cfg, newTestLogger())

  _, err := h.Classify(context.Background(), "test", nil)
  require.Error(t, err)
  assert.ErrorIs(t, err, primaryErr)
}

// --- ClassifyBatch: 폴백 로직 ---

func TestHandler_ClassifyBatch_Fallback활성화_Primary실패_Secondary성공(t *testing.T) {
  // 배치 분류에서 primary 실패 시 fallback=true이면 secondary로 재시도하는지 검증합니다.
  primaryErr := errors.New("primary 배치 오류")
  want := &classifier.BatchClassifyResponse{Total: 2}

  primary := &mockClassifier{
    classifyBatchFn: func(_ context.Context, _ []string, _ []classifier.CategoryInput) (*classifier.BatchClassifyResponse, error) {
      return nil, primaryErr
    },
  }
  secondary := &mockClassifier{
    classifyBatchFn: func(_ context.Context, _ []string, _ []classifier.CategoryInput) (*classifier.BatchClassifyResponse, error) {
      return want, nil
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolGRPC, Fallback: true}
  h := classifier.NewHandler(primary, secondary, cfg, newTestLogger())

  got, err := h.ClassifyBatch(context.Background(), []string{"text1", "text2"}, nil)
  require.NoError(t, err)
  assert.Equal(t, want, got)
}

func TestHandler_ClassifyBatch_Fallback비활성화_Primary실패(t *testing.T) {
  // 배치 분류에서 fallback=false인 경우 primary 에러가 그대로 반환되는지 검증합니다.
  primaryErr := errors.New("primary 배치 오류")
  secondaryCalled := false

  primary := &mockClassifier{
    classifyBatchFn: func(_ context.Context, _ []string, _ []classifier.CategoryInput) (*classifier.BatchClassifyResponse, error) {
      return nil, primaryErr
    },
  }
  secondary := &mockClassifier{
    classifyBatchFn: func(_ context.Context, _ []string, _ []classifier.CategoryInput) (*classifier.BatchClassifyResponse, error) {
      secondaryCalled = true
      return nil, errors.New("secondary는 호출되면 안 됩니다")
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolGRPC, Fallback: false}
  h := classifier.NewHandler(primary, secondary, cfg, newTestLogger())

  _, err := h.ClassifyBatch(context.Background(), []string{"text1"}, nil)
  require.Error(t, err)
  assert.ErrorIs(t, err, primaryErr)
  assert.False(t, secondaryCalled, "fallback=false이면 secondary를 호출하면 안 됩니다")
}

// --- 프로토콜 우선순위: HTTP 우선 ---

func TestHandler_Classify_HTTP우선_성공(t *testing.T) {
  // primary가 HTTP로 설정된 경우 HTTP 클라이언트를 우선 사용하는지 검증합니다.
  want := &classifier.ClassifyResponse{Result: classifier.ClassifyResult{Label: "economy"}}

  grpcCalled := false
  grpcClient := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      grpcCalled = true
      return nil, errors.New("gRPC는 호출되면 안 됩니다")
    },
  }
  httpClient := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return want, nil
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolHTTP, Fallback: false}
  h := classifier.NewHandler(grpcClient, httpClient, cfg, newTestLogger())

  got, err := h.Classify(context.Background(), "test", nil)
  require.NoError(t, err)
  assert.Equal(t, want, got)
  assert.False(t, grpcCalled, "HTTP 우선 설정 시 gRPC를 먼저 호출하면 안 됩니다")
}

func TestHandler_Classify_HTTP우선_실패_GRPC폴백(t *testing.T) {
  // primary=HTTP 실패 시 fallback=true이면 gRPC로 재시도하는지 검증합니다.
  httpErr := errors.New("HTTP 오류")
  want := &classifier.ClassifyResponse{Result: classifier.ClassifyResult{Label: "health"}}

  grpcClient := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return want, nil
    },
  }
  httpClient := &mockClassifier{
    classifyFn: func(_ context.Context, _ string, _ []classifier.CategoryInput) (*classifier.ClassifyResponse, error) {
      return nil, httpErr
    },
  }

  cfg := classifier.HandlerConfig{Primary: classifier.ProtocolHTTP, Fallback: true}
  h := classifier.NewHandler(grpcClient, httpClient, cfg, newTestLogger())

  got, err := h.Classify(context.Background(), "test", nil)
  require.NoError(t, err)
  assert.Equal(t, want, got)
}
