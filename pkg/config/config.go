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

// ValidateConfig는 Content 검증 단계의 임계값 설정을 나타냅니다.
// 뉴스/커뮤니티 소스 타입별로 독립적으로 조정할 수 있습니다.
type ValidateConfig struct {
	// 뉴스 검증 임계값
	NewsTitleMinLen      int     // VALIDATE_NEWS_TITLE_MIN_LEN (default: 10)
	NewsTitleMaxLen      int     // VALIDATE_NEWS_TITLE_MAX_LEN (default: 500)
	NewsBodyMinLen       int     // VALIDATE_NEWS_BODY_MIN_LEN (default: 100)
	NewsBodyMaxLen       int     // VALIDATE_NEWS_BODY_MAX_LEN (default: 50000)
	NewsMinWordCount     int     // VALIDATE_NEWS_MIN_WORD_COUNT (default: 50)
	NewsQualityThreshold float64 // VALIDATE_NEWS_QUALITY_THRESHOLD (default: 0.5)
	NewsMaxCapRatio      float64 // VALIDATE_NEWS_MAX_CAP_RATIO (default: 0.20)
	NewsMaxPunctRatio    float64 // VALIDATE_NEWS_MAX_PUNCT_RATIO (default: 0.10)
	NewsWeightWordCount  float64 // VALIDATE_NEWS_WEIGHT_WORD_COUNT (default: 0.4)
	NewsWeightMeta       float64 // VALIDATE_NEWS_WEIGHT_META (default: 0.3)
	NewsWeightStructure  float64 // VALIDATE_NEWS_WEIGHT_STRUCTURE (default: 0.3)

	// 커뮤니티 검증 임계값
	CommunityBodyMinLen       int     // VALIDATE_COMMUNITY_BODY_MIN_LEN (default: 50)
	CommunityQualityThreshold float64 // VALIDATE_COMMUNITY_QUALITY_THRESHOLD (default: 0.4)
	CommunityMaxRepeatRatio   float64 // VALIDATE_COMMUNITY_MAX_REPEAT_RATIO (default: 0.30)
	CommunityMinRepeatRun     int     // VALIDATE_COMMUNITY_MIN_REPEAT_RUN (default: 4)
}

// DefaultValidateConfig는 개발 환경 기본 ValidateConfig를 반환합니다.
func DefaultValidateConfig() ValidateConfig {
	return ValidateConfig{
		NewsTitleMinLen:           10,
		NewsTitleMaxLen:           500,
		NewsBodyMinLen:            100,
		NewsBodyMaxLen:            50000,
		NewsMinWordCount:          50,
		NewsQualityThreshold:      0.5,
		NewsMaxCapRatio:           0.20,
		NewsMaxPunctRatio:         0.10,
		NewsWeightWordCount:       0.4,
		NewsWeightMeta:            0.3,
		NewsWeightStructure:       0.3,
		CommunityBodyMinLen:       50,
		CommunityQualityThreshold: 0.4,
		CommunityMaxRepeatRatio:   0.30,
		CommunityMinRepeatRun:     4,
	}
}

// LoadValidate는 .env 파일을 로드한 후 OS 환경변수로 ValidateConfig를 구성합니다.
// 환경변수 값이 설정되어 있지만 파싱에 실패하면 에러를 반환합니다.
func LoadValidate(envFiles ...string) (ValidateConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ValidateConfig{}, fmt.Errorf("failed to load env file %q: %w", envFiles[0], err)
	}

	cfg := DefaultValidateConfig()

	parseInt := func(key string, dest *int) error {
		if v := os.Getenv(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("parse %s %q: %w", key, v, err)
			}
			*dest = n
		}
		return nil
	}

	parseFloat := func(key string, dest *float64) error {
		if v := os.Getenv(key); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("parse %s %q: %w", key, v, err)
			}
			*dest = f
		}
		return nil
	}

	for _, op := range []error{
		parseInt("VALIDATE_NEWS_TITLE_MIN_LEN", &cfg.NewsTitleMinLen),
		parseInt("VALIDATE_NEWS_TITLE_MAX_LEN", &cfg.NewsTitleMaxLen),
		parseInt("VALIDATE_NEWS_BODY_MIN_LEN", &cfg.NewsBodyMinLen),
		parseInt("VALIDATE_NEWS_BODY_MAX_LEN", &cfg.NewsBodyMaxLen),
		parseInt("VALIDATE_NEWS_MIN_WORD_COUNT", &cfg.NewsMinWordCount),
		parseFloat("VALIDATE_NEWS_QUALITY_THRESHOLD", &cfg.NewsQualityThreshold),
		parseFloat("VALIDATE_NEWS_MAX_CAP_RATIO", &cfg.NewsMaxCapRatio),
		parseFloat("VALIDATE_NEWS_MAX_PUNCT_RATIO", &cfg.NewsMaxPunctRatio),
		parseFloat("VALIDATE_NEWS_WEIGHT_WORD_COUNT", &cfg.NewsWeightWordCount),
		parseFloat("VALIDATE_NEWS_WEIGHT_META", &cfg.NewsWeightMeta),
		parseFloat("VALIDATE_NEWS_WEIGHT_STRUCTURE", &cfg.NewsWeightStructure),
		parseInt("VALIDATE_COMMUNITY_BODY_MIN_LEN", &cfg.CommunityBodyMinLen),
		parseFloat("VALIDATE_COMMUNITY_QUALITY_THRESHOLD", &cfg.CommunityQualityThreshold),
		parseFloat("VALIDATE_COMMUNITY_MAX_REPEAT_RATIO", &cfg.CommunityMaxRepeatRatio),
		parseInt("VALIDATE_COMMUNITY_MIN_REPEAT_RUN", &cfg.CommunityMinRepeatRun),
	} {
		if op != nil {
			return ValidateConfig{}, op
		}
	}

	return cfg, nil
}

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
		return ClassifierConfig{}, fmt.Errorf("failed to load env file %q: %w", envFiles[0], err)
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
		return DBConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
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
