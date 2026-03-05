// config 패키지는 .env 파일과 환경변수를 통해 IssueTracker 컴포넌트 설정을
// 중앙에서 관리합니다. godotenv.Load() 후 OS 환경변수가 우선 적용됩니다.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

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
		return ClassifierConfig{}, fmt.Errorf("load env file: %w", err)
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

// DBConfig는 PostgreSQL 연결 설정을 나타냅니다.
// 모든 필드는 환경변수(Load 참조)로 덮어쓸 수 있습니다.
type DBConfig struct {
	Host        string
	Port        int
	User        string
	Password    string
	Database    string
	SSLMode     string
	MaxConns    int32
	MinConns    int32
	ConnTimeout time.Duration
}

// DSN은 pgx/v5에서 사용하는 PostgreSQL connection string을 반환합니다.
func (c DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Database, c.SSLMode,
	)
}

// DefaultDBConfig는 로컬 개발 환경용 기본 DBConfig를 반환합니다.
// 프로덕션에서는 환경변수 또는 Load()로 값을 덮어써야 합니다.
func DefaultDBConfig() DBConfig {
	return DBConfig{
		Host:        "localhost",
		Port:        5432,
		User:        "postgres",
		Password:    "postgres",
		Database:    "issuetracker",
		SSLMode:     "disable",
		MaxConns:    25,
		MinConns:    5,
		ConnTimeout: 5 * time.Second,
	}
}

// Load는 .env 파일을 로드한 후 OS 환경변수로 DBConfig를 구성합니다.
// .env 파일이 없으면 무시되고, OS 환경변수가 항상 .env 값보다 우선합니다.
// 환경변수 값이 설정되어 있지만 파싱에 실패하면 에러를 반환합니다.
func Load(envFiles ...string) (DBConfig, error) {
	// .env 파일 로드 (없으면 무시 — 프로덕션에서는 OS env를 직접 사용)
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return DBConfig{}, fmt.Errorf("load env file: %w", err)
	}

	cfg := DefaultDBConfig()

	if v := os.Getenv("POSTGRES_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("POSTGRES_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return DBConfig{}, fmt.Errorf("parse POSTGRES_PORT %q: %w", v, err)
		}
		cfg.Port = p
	}
	if v := os.Getenv("POSTGRES_USER"); v != "" {
		cfg.User = v
	}
	if v := os.Getenv("POSTGRES_PASSWORD"); v != "" {
		cfg.Password = v
	}
	if v := os.Getenv("POSTGRES_DB"); v != "" {
		cfg.Database = v
	}
	if v := os.Getenv("POSTGRES_SSLMODE"); v != "" {
		cfg.SSLMode = v
	}
	if v := os.Getenv("POSTGRES_MAX_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			return DBConfig{}, fmt.Errorf("parse POSTGRES_MAX_CONNS %q: %w", v, err)
		}
		cfg.MaxConns = int32(n)
	}
	if v := os.Getenv("POSTGRES_MIN_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			return DBConfig{}, fmt.Errorf("parse POSTGRES_MIN_CONNS %q: %w", v, err)
		}
		cfg.MinConns = int32(n)
	}
	if v := os.Getenv("POSTGRES_CONN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return DBConfig{}, fmt.Errorf("parse POSTGRES_CONN_TIMEOUT %q: %w", v, err)
		}
		cfg.ConnTimeout = d
	}

	return cfg, nil
}
