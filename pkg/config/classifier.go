package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// ClassifierConfig는 Classifier 서비스 연결 설정을 나타냅니다.
// 모든 필드는 환경변수(LoadClassifier 참조)로 덮어쓸 수 있습니다.
type ClassifierConfig struct {
	HTTPAddr string // HTTP 서버 주소 (예: "http://localhost:8000")
	GRPCAddr string // gRPC 서버 주소 (예: "localhost:50051")
}

// DefaultClassifierConfig는 로컬 개발 환경용 기본 ClassifierConfig를 반환합니다.
func DefaultClassifierConfig() ClassifierConfig {
	return ClassifierConfig{
		HTTPAddr: "http://localhost:8000",
		GRPCAddr: "localhost:50051",
	}
}

// LoadClassifier는 .env 파일을 로드한 후 OS 환경변수로 ClassifierConfig를 구성합니다.
// 지원 환경변수: CLASSIFIER_HTTP_ADDR, CLASSIFIER_GRPC_ADDR
func LoadClassifier(envFiles ...string) (ClassifierConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ClassifierConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultClassifierConfig()

	if v := os.Getenv("CLASSIFIER_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv("CLASSIFIER_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}

	return cfg, nil
}
