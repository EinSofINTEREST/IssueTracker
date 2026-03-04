// Package classifier provides interfaces and implementations for connecting
// to the ELArchive Classifier service via HTTP and gRPC.
//
// classifier 패키지는 ELArchive Classifier 서비스와의 연결을 위한
// 인터페이스와 HTTP/gRPC 구현체를 제공합니다.
//
// HTTP와 gRPC 중 프로토콜을 선택하여 사용하거나, Handler를 통해
// 우선순위 기반 자동 전환(gRPC 우선, HTTP fallback)을 활용할 수 있습니다.
package classifier

import "context"

// CategoryInput은 분류 요청 시 카테고리를 지정하는 타입입니다.
// 생략 시 Classifier 서비스의 기본 카테고리(configs/categories.yaml)를 사용합니다.
type CategoryInput struct {
	Name        string
	Description string
}

// ClassifyResult는 단건 분류 결과입니다.
type ClassifyResult struct {
	Label     string // 예측 카테고리 (예: "politics", "technology")
	Reason    string // LLM 근거 (1문장)
	ParseOk   bool   // LLM 출력 파싱 성공 여부
	RawOutput string // ParseOk=false 시 LLM 원본 출력
}

// ClassifyResponse는 단건 분류 응답입니다.
type ClassifyResponse struct {
	Result ClassifyResult
}

// BatchClassifyItem은 배치 분류 결과 항목입니다.
type BatchClassifyItem struct {
	Index       int
	TextPreview string // 입력 텍스트 앞 100자
	Result      ClassifyResult
}

// BatchClassifyResponse는 배치 분류 응답입니다.
type BatchClassifyResponse struct {
	Total   int
	Results []BatchClassifyItem
}

// HealthResponse는 Classifier 서비스 상태 응답입니다.
type HealthResponse struct {
	Status               string // "ok" | "degraded"
	ModelLoaded          bool
	ModelPath            string
	DefaultCategoryCount int
}

// Classifier는 ELArchive Classifier 서비스와 통신하는 인터페이스입니다.
//
// Classifier is the interface for communicating with the ELArchive Classifier service.
// All implementations must be safe for concurrent use by multiple goroutines.
type Classifier interface {
	// Classify는 텍스트 1건을 분류합니다.
	// categories가 nil이면 서비스 기본 카테고리를 사용합니다.
	Classify(ctx context.Context, text string, categories []CategoryInput) (*ClassifyResponse, error)

	// ClassifyBatch는 텍스트 여러 건을 분류합니다 (최대 100건).
	// categories가 nil이면 서비스 기본 카테고리를 사용합니다.
	ClassifyBatch(ctx context.Context, texts []string, categories []CategoryInput) (*BatchClassifyResponse, error)

	// Health는 Classifier 서비스 상태를 확인합니다.
	Health(ctx context.Context) (*HealthResponse, error)

	// Close는 연결 리소스를 해제합니다.
	Close() error
}
