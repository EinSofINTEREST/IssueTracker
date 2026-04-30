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

// MetricsConfig는 Prometheus /metrics endpoint 노출 설정을 나타냅니다 (이슈 #165).
//
// MetricsConfig holds settings for the Prometheus /metrics HTTP endpoint.
type MetricsConfig struct {
	// Addr: /metrics 노출 listen 주소 (예: ":9090"). 빈 문자열이면 endpoint 비활성화.
	Addr string
}

// DefaultMetricsConfig는 기본 MetricsConfig를 반환합니다 (default Addr ":9090").
func DefaultMetricsConfig() MetricsConfig {
	return MetricsConfig{Addr: ":9090"}
}

// LoadMetrics는 .env 파일을 로드한 후 OS 환경변수로 MetricsConfig를 구성합니다.
// 지원 환경변수: METRICS_ADDR (예: ":9090", 빈 값이면 endpoint 비활성화).
func LoadMetrics(envFiles ...string) (MetricsConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return MetricsConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultMetricsConfig()
	// METRICS_ADDR 가 set 되지 않은 경우 default ":9090" 유지.
	// 빈 문자열로 명시 set ("METRICS_ADDR=") 하면 endpoint 비활성화.
	if v, ok := os.LookupEnv("METRICS_ADDR"); ok {
		cfg.Addr = v
	}
	return cfg, nil
}

// LogConfig는 로거 설정을 나타냅니다.
type LogConfig struct {
	Level  string // LOG_LEVEL: debug | info | warn | error (default: info)
	Pretty bool   // LOG_PRETTY: true | false (default: false)
}

// DefaultLogConfig는 기본 LogConfig를 반환합니다.
func DefaultLogConfig() LogConfig {
	return LogConfig{
		Level:  "info",
		Pretty: false,
	}
}

// LoadLog는 .env 파일을 로드한 후 OS 환경변수로 LogConfig를 구성합니다.
// 지원 환경변수: LOG_LEVEL (debug|info|warn|error), LOG_PRETTY (true|false)
func LoadLog(envFiles ...string) (LogConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return LogConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultLogConfig()

	if v := os.Getenv("LOG_LEVEL"); v != "" {
		switch v {
		case "debug", "info", "warn", "error":
			cfg.Level = v
		default:
			return LogConfig{}, fmt.Errorf("invalid LOG_LEVEL %q: must be one of debug, info, warn, error", v)
		}
	}
	if v := os.Getenv("LOG_PRETTY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return LogConfig{}, fmt.Errorf("parse LOG_PRETTY %q: %w", v, err)
		}
		cfg.Pretty = b
	}

	return cfg, nil
}

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
		return ValidateConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
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

// LLMConfig 는 LLM rule generator (이슈 #149) wiring 설정입니다.
//
// LLMConfig drives the LLM provider used for auto-generating parsing rules when
// a host has no rule registered (rule.ErrNoRule fallback).
//
// 본 PR scope: Gemini 만 사용 (1000회/일 무료 한도) + FixedOrder("gemini") 정책.
// 후속 PR (이슈 TBD) 에서 chain (gemini → openai → anthropic) 으로 확장.
type LLMConfig struct {
	// Enabled: false 면 rule generator wiring 자체 skip (ErrNoRule 잔존 동작 유지).
	// 환경변수 LLM_ENABLED (default true).
	Enabled bool

	// Provider: 사용할 backend 식별자 ("gemini" / "openai" / "anthropic"). default "gemini".
	Provider string

	// APIKey: provider API key. Provider="gemini" 면 GEMINI_API_KEY, openai 면 OPENAI_API_KEY,
	// anthropic 이면 ANTHROPIC_API_KEY 에서 자동 조회. 없으면 LLM_API_KEY fallback.
	APIKey string

	// Model: 호출 기본 모델 (provider default override). default "gemini-2.5-flash".
	Model string

	// Timeout: 단일 LLM 호출 timeout (default 60s).
	Timeout time.Duration
}

// DefaultLLMConfig 는 로컬 개발 환경용 기본 LLMConfig 를 반환합니다.
func DefaultLLMConfig() LLMConfig {
	return LLMConfig{
		Enabled:  true,
		Provider: "gemini",
		Model:    "gemini-2.5-flash",
		Timeout:  60 * time.Second,
	}
}

// LoadLLM 은 .env 를 로드한 후 OS 환경변수로 LLMConfig 를 구성합니다.
//
// 지원 환경변수:
//   - LLM_ENABLED: true | false (default true)
//   - LLM_PROVIDER: gemini | openai | anthropic (default "gemini")
//   - LLM_MODEL: provider-specific model 이름 (default "gemini-2.5-flash")
//   - LLM_TIMEOUT: Go duration (default "60s")
//   - GEMINI_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY: provider 별 key
//   - LLM_API_KEY: 위 provider 별 key 부재 시 fallback (테스트/통합 용)
func LoadLLM(envFiles ...string) (LLMConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return LLMConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultLLMConfig()

	if v := os.Getenv("LLM_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return LLMConfig{}, fmt.Errorf("parse LLM_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("LLM_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("LLM_MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("LLM_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return LLMConfig{}, fmt.Errorf("parse LLM_TIMEOUT %q: %w", v, err)
		}
		cfg.Timeout = d
	}

	cfg.APIKey = lookupLLMAPIKey(cfg.Provider)

	return cfg, nil
}

// lookupLLMAPIKey 는 provider 별 표준 환경변수에서 API key 를 조회하고,
// 부재 시 LLM_API_KEY fallback 을 사용합니다.
func lookupLLMAPIKey(provider string) string {
	switch provider {
	case "gemini":
		if v := os.Getenv("GEMINI_API_KEY"); v != "" {
			return v
		}
	case "openai":
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			return v
		}
	case "anthropic", "claude":
		if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
			return v
		}
	}
	return os.Getenv("LLM_API_KEY")
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

// RedisConfig는 Redis 연결 설정을 나타냅니다.
// 모든 필드는 환경변수(LoadRedis 참조)로 덮어쓸 수 있습니다.
type RedisConfig struct {
	Host         string        // REDIS_HOST (default: localhost)
	Port         int           // REDIS_PORT (default: 6379)
	Password     string        // REDIS_PASSWORD (default: "")
	DB           int           // REDIS_DB (default: 0)
	DialTimeout  time.Duration // REDIS_DIAL_TIMEOUT (default: 5s)
	ReadTimeout  time.Duration // REDIS_READ_TIMEOUT (default: 3s)
	WriteTimeout time.Duration // REDIS_WRITE_TIMEOUT (default: 3s)
	PoolSize     int           // REDIS_POOL_SIZE (default: 10)
	URLCacheTTL  time.Duration // REDIS_URL_CACHE_TTL (default: 24h)
}

// DefaultRedisConfig는 로컬 개발 환경용 기본 RedisConfig를 반환합니다.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		Host:         "localhost",
		Port:         6379,
		Password:     "",
		DB:           0,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
		URLCacheTTL:  24 * time.Hour,
	}
}

// LoadRedis는 .env 파일을 로드한 후 OS 환경변수로 RedisConfig를 구성합니다.
// 환경변수 값이 설정되어 있지만 파싱에 실패하거나 범위를 벗어나면 에러를 반환합니다.
func LoadRedis(envFiles ...string) (RedisConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RedisConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultRedisConfig()

	if v := os.Getenv("REDIS_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("REDIS_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_PORT %q: %w", v, err)
		}
		if p < 1 || p > 65535 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_PORT %d: must be between 1 and 65535", p)
		}
		cfg.Port = p
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Password = v
	}
	if v := os.Getenv("REDIS_DB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_DB %q: %w", v, err)
		}
		if n < 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_DB %d: must be 0 or greater", n)
		}
		cfg.DB = n
	}
	if v := os.Getenv("REDIS_DIAL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_DIAL_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_DIAL_TIMEOUT %q: must be positive", v)
		}
		cfg.DialTimeout = d
	}
	if v := os.Getenv("REDIS_READ_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_READ_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_READ_TIMEOUT %q: must be positive", v)
		}
		cfg.ReadTimeout = d
	}
	if v := os.Getenv("REDIS_WRITE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_WRITE_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_WRITE_TIMEOUT %q: must be positive", v)
		}
		cfg.WriteTimeout = d
	}
	if v := os.Getenv("REDIS_POOL_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_POOL_SIZE %q: %w", v, err)
		}
		if n < 1 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_POOL_SIZE %d: must be 1 or greater", n)
		}
		cfg.PoolSize = n
	}
	if v := os.Getenv("REDIS_URL_CACHE_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_URL_CACHE_TTL %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_URL_CACHE_TTL %q: must be positive", v)
		}
		cfg.URLCacheTTL = d
	}

	return cfg, nil
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

// SchedulerConfig는 Job Scheduler의 크롤 주기 설정을 나타냅니다.
// 소스 타입별로 독립적으로 조정할 수 있습니다.
type SchedulerConfig struct {
	CategoryInterval time.Duration // 카테고리 목록 폴링 주기 — SCHEDULER_CATEGORY_INTERVAL (default: 2h)
	JobTimeout       time.Duration // 개별 Job 최대 실행 시간 — SCHEDULER_JOB_TIMEOUT (default: 30s)
	MaxRetries       int           // Job 최대 재시도 횟수 — SCHEDULER_MAX_RETRIES (default: 3)

	// Backlog throttle (이슈 #124): publish 직전 Kafka crawl 토픽의
	// consumer-group lag 가 임계값 초과 시 발행 차단.
	// MaxBacklog <= 0 → throttle 비활성 (기본).
	MaxBacklog          int64         // SCHEDULER_MAX_BACKLOG (default: 0 — disabled)
	BacklogCheckTimeout time.Duration // SCHEDULER_BACKLOG_CHECK_TIMEOUT (default: 5s)
}

// DefaultSchedulerConfig는 기본 SchedulerConfig를 반환합니다.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		CategoryInterval:    2 * time.Hour,
		JobTimeout:          30 * time.Second,
		MaxRetries:          3,
		MaxBacklog:          0, // disabled by default — opt-in via env
		BacklogCheckTimeout: 5 * time.Second,
	}
}

// LoadScheduler는 .env 파일을 로드한 후 OS 환경변수로 SchedulerConfig를 구성합니다.
// 환경변수 값이 설정되어 있지만 파싱에 실패하면 에러를 반환합니다.
func LoadScheduler(envFiles ...string) (SchedulerConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SchedulerConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultSchedulerConfig()

	parseDuration := func(key string, dest *time.Duration) error {
		if v := os.Getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("parse %s %q: %w", key, v, err)
			}
			*dest = d
		}
		return nil
	}

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

	parseInt64 := func(key string, dest *int64) error {
		if v := os.Getenv(key); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return fmt.Errorf("parse %s %q: %w", key, v, err)
			}
			*dest = n
		}
		return nil
	}

	for _, op := range []error{
		parseDuration("SCHEDULER_CATEGORY_INTERVAL", &cfg.CategoryInterval),
		parseDuration("SCHEDULER_JOB_TIMEOUT", &cfg.JobTimeout),
		parseInt("SCHEDULER_MAX_RETRIES", &cfg.MaxRetries),
		parseInt64("SCHEDULER_MAX_BACKLOG", &cfg.MaxBacklog),
		parseDuration("SCHEDULER_BACKLOG_CHECK_TIMEOUT", &cfg.BacklogCheckTimeout),
	} {
		if op != nil {
			return SchedulerConfig{}, op
		}
	}

	return cfg, nil
}
