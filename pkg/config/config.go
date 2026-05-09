// config 패키지는 .env 파일과 환경변수를 통해 IssueTracker 컴포넌트 설정을
// 중앙에서 관리합니다. godotenv.Load() 후 OS 환경변수가 우선 적용됩니다.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// MetricsConfig는 Prometheus /metrics endpoint 노출 설정을 나타냅니다.
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

// ShutdownConfig 는 graceful shutdown 시 대기 시간 설정입니다.
//
// 배경: claudegen LLM 추출기 활성 시 in-flight Extract 호출 latency 가 p95 110s 까지 늘어나,
// 기존 hardcode 30s shutdownCtx 가 정기적으로 deadline exceeded 를 트리거. 운영자가
// 사용 중인 LLM provider latency 에 맞춰 timeout 을 조정할 수 있도록 외부화.
type ShutdownConfig struct {
	// Timeout: stages.Stop / worker.Stop 전체 timeout (default 30s).
	// claudegen 활성 환경에서는 120s 권장.
	Timeout time.Duration

	// ClaudegenTimeout: claudegen 컨테이너 cleanup (docker rm -f) timeout (default 10s).
	// docker 명령 자체는 빠르나, daemon 응답 지연 케이스 대비 환경변수화.
	ClaudegenTimeout time.Duration
}

// DefaultShutdownConfig 는 기본 ShutdownConfig 를 반환합니다.
func DefaultShutdownConfig() ShutdownConfig {
	return ShutdownConfig{
		Timeout:          30 * time.Second,
		ClaudegenTimeout: 10 * time.Second,
	}
}

// LoadShutdown 은 .env 파일을 로드한 후 OS 환경변수로 ShutdownConfig 를 구성합니다.
// 지원 환경변수:
//   - SHUTDOWN_TIMEOUT (default 30s)
//   - CLAUDE_CODE_SHUTDOWN_TIMEOUT (default 10s)
func LoadShutdown(envFiles ...string) (ShutdownConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ShutdownConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultShutdownConfig()

	parseDuration := func(key string, dest *time.Duration) error {
		v := os.Getenv(key)
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("parse %s %q: %w", key, v, err)
		}
		if d <= 0 {
			return fmt.Errorf("%s must be positive, got %s", key, d)
		}
		*dest = d
		return nil
	}

	if err := parseDuration("SHUTDOWN_TIMEOUT", &cfg.Timeout); err != nil {
		return ShutdownConfig{}, err
	}
	if err := parseDuration("CLAUDE_CODE_SHUTDOWN_TIMEOUT", &cfg.ClaudegenTimeout); err != nil {
		return ShutdownConfig{}, err
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

// PathInferConfig 는 pathinfer 휴리스틱의 설정입니다.
//
// pathinfer 패키지의 InferHeuristic 동작을 운영자가 환경변수로 조정 가능하도록.
type PathInferConfig struct {
	// MinSamples: 추론에 필요한 최소 sample URL 수.
	// 환경변수: PATHINFER_MIN_SAMPLES (default 3).
	// 너무 낮으면 (1-2) 공통 vs 변수 구분 의미 없음, 너무 높으면 (10+) sample 수집 지연.
	MinSamples int
}

// DefaultPathInferConfig 는 기본 PathInferConfig 를 반환합니다.
func DefaultPathInferConfig() PathInferConfig {
	return PathInferConfig{
		MinSamples: 3,
	}
}

// LoadPathInfer 는 .env 를 로드한 후 OS 환경변수로 PathInferConfig 를 구성합니다.
// 지원 환경변수: PATHINFER_MIN_SAMPLES (양의 정수, default 3).
func LoadPathInfer(envFiles ...string) (PathInferConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return PathInferConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultPathInferConfig()

	if v := os.Getenv("PATHINFER_MIN_SAMPLES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return PathInferConfig{}, fmt.Errorf("parse PATHINFER_MIN_SAMPLES %q: %w", v, err)
		}
		if n < 1 {
			return PathInferConfig{}, fmt.Errorf("invalid PATHINFER_MIN_SAMPLES %d: must be 1 or greater", n)
		}
		cfg.MinSamples = n
	}

	return cfg, nil
}

// FetcherChromedpPoolConfig 는 chromedp 전용 worker pool 의 wiring 설정입니다.
//
// goquery worker pool 과 분리된 별도 Kafka consumer group 을 운영하며, worker 의 chromedp 호출
// 직전에 Semaphore.Acquire 로 Chrome 인스턴스의 동시 navigation 수를 제한해 ResourceScheduler
// 큐 고갈 (ERR_INSUFFICIENT_RESOURCES) 을 차단.
//
// Semaphore 의미 변경 + 실효 동시성 정정 (gemini 피드백):
// 글로벌 Semaphore 1 개 (전체 worker 공유) 모델에서, worker_id 별 Semaphore 1 개로
// 분리. **단, KafkaConsumerPool 의 worker goroutine 은 jobs 채널에서 메시지를 1 개씩 꺼내
// 순차 처리** 하므로 (pool.go 의 worker 루프 참조), 같은 worker 가 동시 2 개 이상의 Handle 을
// 호출할 수 없음 → per-worker SemaphoreCapacity > 1 은 현 모델에서 추가 동시성 이득 없음.
//
// 실질 전체 동시성 = WorkerCount (NOT WorkerCount × SemaphoreCapacity).
// SemaphoreCapacity > 1 옵션은 미래 worker-내 동시 분기 (예: 한 워커가 받은 메시지를 여러 tab
// 으로 분할 처리) 시나리오 대비 보존 — 현 운영에서는 default 1 권장.
//
// 다음 sub-issue (#230) 에서 RemoteURL 매핑까지 N 개로 확장하면 worker:Chrome 1:1 모델 활성화.
type FetcherChromedpPoolConfig struct {
	// Enabled: false 면 chromedp pool 미기동. **주의**: goquery worker 의 ChainHandler 가 lazy
	// detect / chromedp 룰 / force_fetcher 분기에서 항상 TopicCrawlChromedp 로 republish 하므로
	// pool 미기동 상태에서는 그 메시지가 처리되지 않고 누적된다 — main.go 가 fail-fast.
	// 운영자가 chromedp 처리를 진정으로 비활성화하려면 chain_handler 의 republish 분기도 함께
	// fork 해야 함 (별도 PR).
	// 환경변수 FETCHER_CHROMEDP_POOL_ENABLED (default true).
	Enabled bool

	// WorkerCount: chromedp pool 의 worker goroutine 수.
	// **실질 전체 동시 navigate 수 = WorkerCount** (per-worker 순차 처리 모델). 처리량 튜닝의
	// 단일 lever 이므로 운영 부하에 맞춰 조정 — sub-issue #230 머지 후에는 RemoteURLs 수와
	// 일치시켜야 함 (worker:Chrome 1:1 매핑 보장).
	// 환경변수 FETCHER_CHROMEDP_WORKER_COUNT (default 2).
	WorkerCount int

	// SemaphoreCapacity: per-worker Semaphore 슬롯 수.
	// **현 KafkaConsumerPool 모델에서는 1 이상 의미 없음** — worker 가 메시지를 순차 처리하므로
	// 같은 worker 의 동시 Acquire 가 발생하지 않음. 옵션은 미래 worker-내 동시 분기 시나리오
	// (예: 한 worker 가 메시지 1건을 여러 sub-tab 으로 분할) 대비 보존.
	// 운영 처리량 조정은 SemaphoreCapacity 가 아닌 WorkerCount 로 수행.
	// 환경변수 FETCHER_CHROMEDP_SEMAPHORE_CAPACITY (default 1).
	SemaphoreCapacity int

	// RemoteURLs: worker_id 별 Chrome 인스턴스의 CDP WebSocket URL 매핑.
	// 길이는 WorkerCount 와 일치해야 하며 (LoadFetcherChromedpPool 가 검증), 사이트별 ChromedpCrawler
	// 가 worker_id 인덱스로 자기 전용 RemoteURL 을 사용 → worker:Chrome 1:1 매핑.
	//
	// 우선순위 (LoadFetcherChromedpPool 가 채움):
	//  1. FETCHER_CHROMEDP_REMOTE_URLS (콤마 구분 list, 명시적 매핑)
	//  2. FETCHER_CHROMEDP_REMOTE_URL_PATTERN ({n} placeholder, 1..WorkerCount 치환)
	//  3. default (ws://localhost:9222 × WorkerCount — 단일 Chrome 호환)
	RemoteURLs []string
}

// DefaultFetcherChromedpPoolConfig 는 기본 FetcherChromedpPoolConfig 를 반환합니다.
//
// default 재조정 (gemini 피드백 반영):
// SemaphoreCapacity 가 현 KafkaConsumerPool 순차 처리 모델에서 1 이상 의미 없으므로 default 1.
// 기존 운영자가 환경변수로 4 를 명시했다면 — 그 값은 무시되지 않고 그대로 적용되지만 (slot 수
// 4 의 sem 이 worker 별로 만들어짐) 실효 동시성은 변하지 않음 (worker 1 + slot 4 = 동시 1건).
// 처리량 변경은 WorkerCount 환경변수로만 조정 가능.
//
// RemoteURLs default:
// FETCHER_CHROMEDP_REMOTE_URLS 미지정 시 LoadFetcherChromedpPool 가 ws://localhost:9222 을
// WorkerCount 만큼 복제하여 채움 — 기존 단일 Chrome 운영 호환 (이전 동작 100% 보존).
// Default 함수 자체는 빈 slice 반환 — Load 가 WorkerCount 결정 후 채움.
func DefaultFetcherChromedpPoolConfig() FetcherChromedpPoolConfig {
	return FetcherChromedpPoolConfig{
		Enabled:           true,
		WorkerCount:       2,
		SemaphoreCapacity: 1,
		RemoteURLs:        nil,
	}
}

// LoadFetcherChromedpPool 는 .env 를 로드한 후 OS 환경변수로 FetcherChromedpPoolConfig 를 구성합니다.
//
// 지원 환경변수:
//   - FETCHER_CHROMEDP_POOL_ENABLED: true | false (default true)
//   - FETCHER_CHROMEDP_WORKER_COUNT: 양의 정수 (default 2) — 실질 전체 동시 navigate 수
//   - FETCHER_CHROMEDP_SEMAPHORE_CAPACITY: 양의 정수, per-worker (default 1, 1 이상 의미 없음)
//   - FETCHER_CHROMEDP_REMOTE_URLS: 콤마 구분 CDP WS URL 리스트. 명시 시 가장 우선.
//     길이가 WorkerCount 와 일치해야 함 — 미일치 시 fail-fast.
//   - FETCHER_CHROMEDP_REMOTE_URL_PATTERN: {n} placeholder 를 1..WorkerCount 로 치환하여 RemoteURLs
//     자동 생성 (예: "ws://chrome-{n}:9222"). REMOTE_URLS 미지정 시 적용. {n} 누락 시 fail-fast
//     (모든 worker 가 같은 url 로 향해 1:1 매핑이 무력화되는 사고 방지).
//   - 둘 다 미지정 시 default ws://localhost:9222 × WorkerCount — 단일 Chrome 호환.
func LoadFetcherChromedpPool(envFiles ...string) (FetcherChromedpPoolConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return FetcherChromedpPoolConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultFetcherChromedpPoolConfig()

	if v := os.Getenv("FETCHER_CHROMEDP_POOL_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return FetcherChromedpPoolConfig{}, fmt.Errorf("parse FETCHER_CHROMEDP_POOL_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("FETCHER_CHROMEDP_WORKER_COUNT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return FetcherChromedpPoolConfig{}, fmt.Errorf("parse FETCHER_CHROMEDP_WORKER_COUNT %q: %w", v, err)
		}
		if n < 1 {
			return FetcherChromedpPoolConfig{}, fmt.Errorf("invalid FETCHER_CHROMEDP_WORKER_COUNT %d: must be 1 or greater", n)
		}
		cfg.WorkerCount = n
	}
	if v := os.Getenv("FETCHER_CHROMEDP_SEMAPHORE_CAPACITY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return FetcherChromedpPoolConfig{}, fmt.Errorf("parse FETCHER_CHROMEDP_SEMAPHORE_CAPACITY %q: %w", v, err)
		}
		if n < 1 {
			return FetcherChromedpPoolConfig{}, fmt.Errorf("invalid FETCHER_CHROMEDP_SEMAPHORE_CAPACITY %d: must be 1 or greater", n)
		}
		cfg.SemaphoreCapacity = n
	}

	// RemoteURLs 처리 — 우선순위:
	//   1. FETCHER_CHROMEDP_REMOTE_URLS (콤마 구분 list, 명시적 매핑)
	//   2. FETCHER_CHROMEDP_REMOTE_URL_PATTERN ({n} placeholder, 1..WorkerCount 치환)
	//   3. default (ws://localhost:9222 × WorkerCount — 단일 Chrome 호환)
	//
	// PATTERN 모드는 docker compose --scale chrome=N 운영과 결합 — WorkerCount 만 조정하면
	// RemoteURLs 도 자동으로 N 개 생성. 운영자가 두 env 를 따로 동기화할 필요 없음.
	if v := os.Getenv("FETCHER_CHROMEDP_REMOTE_URLS"); v != "" {
		parts := strings.Split(v, ",")
		urls := make([]string, 0, len(parts))
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if trimmed == "" {
				return FetcherChromedpPoolConfig{}, fmt.Errorf("invalid FETCHER_CHROMEDP_REMOTE_URLS %q: empty entry detected", v)
			}
			urls = append(urls, trimmed)
		}
		if len(urls) != cfg.WorkerCount {
			return FetcherChromedpPoolConfig{}, fmt.Errorf("invalid FETCHER_CHROMEDP_REMOTE_URLS: got %d url(s), want %d (= FETCHER_CHROMEDP_WORKER_COUNT)", len(urls), cfg.WorkerCount)
		}
		cfg.RemoteURLs = urls
	} else if pattern := strings.TrimSpace(os.Getenv("FETCHER_CHROMEDP_REMOTE_URL_PATTERN")); pattern != "" {
		// {n} placeholder 누락 시 모든 worker 가 같은 url 로 향함 — 1:1 매핑이 사실상 무력화.
		// fail-fast 로 운영자가 명시적 의사결정 (REMOTE_URLS 사용 또는 PATTERN 에 {n} 추가) 강제.
		if !strings.Contains(pattern, "{n}") {
			return FetcherChromedpPoolConfig{}, fmt.Errorf("invalid FETCHER_CHROMEDP_REMOTE_URL_PATTERN %q: missing {n} placeholder (use REMOTE_URLS for static mapping or add {n} for per-worker substitution)", pattern)
		}
		urls := make([]string, cfg.WorkerCount)
		for i := 0; i < cfg.WorkerCount; i++ {
			// 1-indexed: docker compose 의 chrome-1, chrome-2, ... 명명 규칙과 일치
			urls[i] = strings.ReplaceAll(pattern, "{n}", strconv.Itoa(i+1))
		}
		cfg.RemoteURLs = urls
	} else {
		// Default: 단일 Chrome 호환 (이전 동작 100% 보존). worker 가 N>1 이어도 같은 Chrome 공유.
		// → worker:Chrome 1:1 격리 효과는 사라지지만 graceful default — 운영자가 명시적 RemoteURLs
		// 또는 REMOTE_URL_PATTERN 지정으로 1:1 활성화.
		cfg.RemoteURLs = make([]string, cfg.WorkerCount)
		for i := range cfg.RemoteURLs {
			cfg.RemoteURLs[i] = "ws://localhost:9222"
		}
	}

	return cfg, nil
}

// FetcherAutoDowngradeConfig 는 자동 upgrade 된 host 를 주기적으로 goquery 로 되돌리는 안전장치 설정입니다.
//
// upgrade-only 비대칭으로 인해 일시적 트래픽 에러로 잘못 upgrade 된 host 가 영원히 chromedp 로
// 처리되어 시간 누적 시 모든 host 가 chromedp 로 수렴 → Chrome 자원 압박. 본 cron 이 주기적
// 으로 reason='auto_upgrade_validation' row 를 일괄 goquery 로 reset 한다.
// 진짜 SPA host 는 단계 2 의 카운터가 24h 내 재upgrade 하므로 영향 없음. manual 룰은 보호.
type FetcherAutoDowngradeConfig struct {
	// Enabled: false 면 cron 자체 비활성. 환경변수 FETCHER_AUTO_DOWNGRADE_ENABLED (default true).
	Enabled bool

	// Interval: downgrade cron 실행 주기. 환경변수 FETCHER_AUTO_DOWNGRADE_INTERVAL (Go duration, default 168h = 7일).
	// 너무 짧으면 정상 chromedp host 가 자주 reset 되어 fetch latency ↑ — 24h 이상 권장.
	Interval time.Duration
}

// DefaultFetcherAutoDowngradeConfig 는 기본 FetcherAutoDowngradeConfig 를 반환합니다.
func DefaultFetcherAutoDowngradeConfig() FetcherAutoDowngradeConfig {
	return FetcherAutoDowngradeConfig{
		Enabled:  true,
		Interval: 7 * 24 * time.Hour,
	}
}

// LoadFetcherAutoDowngrade 는 .env 를 로드한 후 OS 환경변수로 FetcherAutoDowngradeConfig 를 구성합니다.
//
// 지원 환경변수:
//   - FETCHER_AUTO_DOWNGRADE_ENABLED: true | false (default true)
//   - FETCHER_AUTO_DOWNGRADE_INTERVAL: Go duration (default "168h" = 7일)
func LoadFetcherAutoDowngrade(envFiles ...string) (FetcherAutoDowngradeConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return FetcherAutoDowngradeConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultFetcherAutoDowngradeConfig()

	if v := os.Getenv("FETCHER_AUTO_DOWNGRADE_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return FetcherAutoDowngradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_DOWNGRADE_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("FETCHER_AUTO_DOWNGRADE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return FetcherAutoDowngradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_DOWNGRADE_INTERVAL %q: %w", v, err)
		}
		if d <= 0 {
			return FetcherAutoDowngradeConfig{}, fmt.Errorf("invalid FETCHER_AUTO_DOWNGRADE_INTERVAL %q: must be positive", v)
		}
		cfg.Interval = d
	}

	return cfg, nil
}

// FetcherAutoUpgradeConfig 는 host 단위 fetcher 실패 누적 → chromedp 자동 전환 정책 설정입니다.
//
// 본 단계는 카운팅 + 임계값 도달 신호 발신까지만 — 실제 fetcher_rules UPSERT / 실패 raw republish 는 단계 3 (#221) 의 책임.
//
// Window / Threshold 의 의미:
//   - 윈도우 (default 1h) 안에 같은 host 의 실패가 Threshold (default 5) 회 누적되면 trigger.
//   - 윈도우는 sliding — 마지막 실패 시각에서 Window 만큼 이전까지 카운트.
type FetcherAutoUpgradeConfig struct {
	// Enabled: false 면 카운팅 자체 skip (Noop FailureCounter 사용 — 성능 저하 0).
	// 환경변수 FETCHER_AUTO_UPGRADE_ENABLED (default true).
	Enabled bool

	// Threshold: window 내 실패 횟수 임계값 (이상이면 trigger). 환경변수 FETCHER_AUTO_UPGRADE_THRESHOLD (default 5).
	Threshold int

	// Window: sliding window 길이. 환경변수 FETCHER_AUTO_UPGRADE_WINDOW (Go duration, default 1h).
	Window time.Duration

	// EmptyBodyTitleMin / EmptyBodyContentMin: 빈본문 판정 임계값.
	// parse 자체는 성공했지만 결과 텍스트가 너무 짧은 경우도 실패 신호로 카운팅.
	// 환경변수 FETCHER_EMPTY_BODY_TITLE_MIN (default 5), FETCHER_EMPTY_BODY_CONTENT_MIN (default 100).
	EmptyBodyTitleMin   int
	EmptyBodyContentMin int
}

// DefaultFetcherAutoUpgradeConfig 는 기본 FetcherAutoUpgradeConfig 를 반환합니다.
func DefaultFetcherAutoUpgradeConfig() FetcherAutoUpgradeConfig {
	return FetcherAutoUpgradeConfig{
		Enabled:             true,
		Threshold:           5,
		Window:              1 * time.Hour,
		EmptyBodyTitleMin:   5,
		EmptyBodyContentMin: 100,
	}
}

// LoadFetcherAutoUpgrade 는 .env 를 로드한 후 OS 환경변수로 FetcherAutoUpgradeConfig 를 구성합니다.
//
// 지원 환경변수:
//   - FETCHER_AUTO_UPGRADE_ENABLED: true | false (default true)
//   - FETCHER_AUTO_UPGRADE_THRESHOLD: 양의 정수 (default 5)
//   - FETCHER_AUTO_UPGRADE_WINDOW: Go duration (default "1h")
//   - FETCHER_EMPTY_BODY_TITLE_MIN: 0 이상의 정수 (default 5)
//   - FETCHER_EMPTY_BODY_CONTENT_MIN: 0 이상의 정수 (default 100)
func LoadFetcherAutoUpgrade(envFiles ...string) (FetcherAutoUpgradeConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return FetcherAutoUpgradeConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultFetcherAutoUpgradeConfig()

	if v := os.Getenv("FETCHER_AUTO_UPGRADE_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_UPGRADE_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("FETCHER_AUTO_UPGRADE_THRESHOLD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_UPGRADE_THRESHOLD %q: %w", v, err)
		}
		if n < 1 {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("invalid FETCHER_AUTO_UPGRADE_THRESHOLD %d: must be 1 or greater", n)
		}
		cfg.Threshold = n
	}
	if v := os.Getenv("FETCHER_AUTO_UPGRADE_WINDOW"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_UPGRADE_WINDOW %q: %w", v, err)
		}
		if d <= 0 {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("invalid FETCHER_AUTO_UPGRADE_WINDOW %q: must be positive", v)
		}
		cfg.Window = d
	}
	if v := os.Getenv("FETCHER_EMPTY_BODY_TITLE_MIN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_EMPTY_BODY_TITLE_MIN %q: %w", v, err)
		}
		if n < 0 {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("invalid FETCHER_EMPTY_BODY_TITLE_MIN %d: must be 0 or greater", n)
		}
		cfg.EmptyBodyTitleMin = n
	}
	if v := os.Getenv("FETCHER_EMPTY_BODY_CONTENT_MIN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_EMPTY_BODY_CONTENT_MIN %q: %w", v, err)
		}
		if n < 0 {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("invalid FETCHER_EMPTY_BODY_CONTENT_MIN %d: must be 0 or greater", n)
		}
		cfg.EmptyBodyContentMin = n
	}

	return cfg, nil
}

// StaleRelearnConfig 는 stale rule (parse_failure / empty_selector) 누적 → LLM 자동 재학습 정책 설정입니다.
//
// FetcherAutoUpgrade 와 별개의 keyspace + 더 긴 윈도우 / 더 높은 임계값 — chromedp 자동 전환을
// 먼저 시도한 후, 그래도 실패가 지속되면 LLM 재학습 트리거. 임계 도달 시 Generator.EnqueueStale
// 가 InsertNextVersion 으로 동일 자연키 (source, host, path, type) 의 다음 버전을 INSERT.
type StaleRelearnConfig struct {
	// Enabled: false 면 카운팅 자체 skip (Noop StaleCounter — 성능 저하 0).
	// 환경변수 STALE_RELEARN_ENABLED (default true).
	Enabled bool

	// Threshold: 윈도우 내 stale parse failure 임계값. 환경변수 STALE_RELEARN_THRESHOLD (default 10).
	// chromedp 자동 전환 (default 5) 보다 높게 — chromedp 가 먼저 시도되도록.
	Threshold int

	// Window: sliding window 길이. 환경변수 STALE_RELEARN_WINDOW (Go duration, default 2h).
	// chromedp 자동 전환 (default 1h) 보다 길게 — 더 보수적 관찰 기간.
	Window time.Duration
}

// DefaultStaleRelearnConfig 는 기본 StaleRelearnConfig 를 반환합니다.
func DefaultStaleRelearnConfig() StaleRelearnConfig {
	return StaleRelearnConfig{
		Enabled:   true,
		Threshold: 10,
		Window:    2 * time.Hour,
	}
}

// LoadStaleRelearn 는 .env 를 로드한 후 OS 환경변수로 StaleRelearnConfig 를 구성합니다.
//
// 지원 환경변수:
//   - STALE_RELEARN_ENABLED   : true | false (default true)
//   - STALE_RELEARN_THRESHOLD : 양의 정수 (default 10)
//   - STALE_RELEARN_WINDOW    : Go duration (default "2h")
func LoadStaleRelearn(envFiles ...string) (StaleRelearnConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return StaleRelearnConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultStaleRelearnConfig()

	if v := os.Getenv("STALE_RELEARN_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return StaleRelearnConfig{}, fmt.Errorf("parse STALE_RELEARN_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("STALE_RELEARN_THRESHOLD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return StaleRelearnConfig{}, fmt.Errorf("parse STALE_RELEARN_THRESHOLD %q: %w", v, err)
		}
		if n < 1 {
			return StaleRelearnConfig{}, fmt.Errorf("invalid STALE_RELEARN_THRESHOLD %d: must be 1 or greater", n)
		}
		cfg.Threshold = n
	}
	if v := os.Getenv("STALE_RELEARN_WINDOW"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return StaleRelearnConfig{}, fmt.Errorf("parse STALE_RELEARN_WINDOW %q: %w", v, err)
		}
		if d <= 0 {
			return StaleRelearnConfig{}, fmt.Errorf("invalid STALE_RELEARN_WINDOW %q: must be positive", v)
		}
		cfg.Window = d
	}

	return cfg, nil
}

// BlacklistConfig 는 page-parse 블랙리스트 wiring 설정입니다.
//
// Enabled=false 면 BlacklistMatcher 미주입 — parser_worker.processCategoryPage 가 모든 카테고리
// 링크를 그대로 article job 으로 발행 (기능 OFF). 운영 toggle 용도.
type BlacklistConfig struct {
	// Enabled: 환경변수 BLACKLIST_ENABLED (default true).
	Enabled bool
}

// DefaultBlacklistConfig 는 기본 BlacklistConfig 를 반환합니다.
func DefaultBlacklistConfig() BlacklistConfig {
	return BlacklistConfig{Enabled: true}
}

// LoadBlacklist 는 .env 를 로드한 후 OS 환경변수로 BlacklistConfig 를 구성합니다.
//
// 지원 환경변수:
//   - BLACKLIST_ENABLED: true | false (default true)
func LoadBlacklist(envFiles ...string) (BlacklistConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return BlacklistConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultBlacklistConfig()
	if v := os.Getenv("BLACKLIST_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return BlacklistConfig{}, fmt.Errorf("parse BLACKLIST_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	return cfg, nil
}

// LLMConfig 는 LLM rule generator wiring 설정입니다.
//
// LLMConfig drives the LLM provider used for auto-generating parsing rules when
// a host has no rule registered (rule.ErrNoRule fallback).
//
// Gemini 만 사용 (1000회/일 무료 한도) + FixedOrder("gemini") 정책.
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

// RefinementConfig 는 점진적 정밀화 워크플로의 설정입니다.
//
// catch-all + llm-auto rule 의 누적 sample URL 로부터 path_pattern 을 추론하여 자동 갱신.
//
// 환경변수:
//   - REFINEMENT_ENABLED: true | false (default true) — 비활성 시 background goroutine 시작 X
//   - REFINEMENT_INTERVAL: polling 주기 (default 5m)
//   - REFINEMENT_MIN_SAMPLES: rule 당 트리거 임계값 (default 5)
type RefinementConfig struct {
	Enabled    bool
	Interval   time.Duration
	MinSamples int
}

// DefaultRefinementConfig 는 기본 RefinementConfig 를 반환합니다.
func DefaultRefinementConfig() RefinementConfig {
	return RefinementConfig{
		Enabled:    true,
		Interval:   5 * time.Minute,
		MinSamples: 5,
	}
}

// LoadRefinement 는 .env 를 로드한 후 OS 환경변수로 RefinementConfig 를 구성합니다.
func LoadRefinement(envFiles ...string) (RefinementConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RefinementConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultRefinementConfig()

	if v := os.Getenv("REFINEMENT_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return RefinementConfig{}, fmt.Errorf("parse REFINEMENT_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("REFINEMENT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RefinementConfig{}, fmt.Errorf("parse REFINEMENT_INTERVAL %q: %w", v, err)
		}
		if d <= 0 {
			return RefinementConfig{}, fmt.Errorf("invalid REFINEMENT_INTERVAL %q: must be positive", v)
		}
		cfg.Interval = d
	}
	if v := os.Getenv("REFINEMENT_MIN_SAMPLES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return RefinementConfig{}, fmt.Errorf("parse REFINEMENT_MIN_SAMPLES %q: %w", v, err)
		}
		if n < 1 {
			return RefinementConfig{}, fmt.Errorf("invalid REFINEMENT_MIN_SAMPLES %d: must be 1 or greater", n)
		}
		cfg.MinSamples = n
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
	// IngestionLockTTL: 파이프라인 진입 marker 의 TTL.
	// publisher 가 atomic SETNX 로 marker 를 잡고, 본 TTL 만료 시 자연스럽게 재크롤 가능.
	// 환경변수: REDIS_INGESTION_LOCK_TTL (default 24h).
	IngestionLockTTL time.Duration

	// PipelineGuardCategoryTTL: PipelineGuard 의 Category target 전용 단명 TTL.
	// fetch + ParseLinks 한 cycle 진행 중에만 marker 유지 — 정상 흐름은 명시적 Release,
	// 본 TTL 은 fallback (worker 가 release 호출 못 하고 죽은 경우 자동 회수).
	// 환경변수: PIPELINE_GUARD_CATEGORY_TTL (default 60s).
	PipelineGuardCategoryTTL time.Duration
}

// DefaultRedisConfig는 로컬 개발 환경용 기본 RedisConfig를 반환합니다.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		Host:                     "localhost",
		Port:                     6379,
		Password:                 "",
		DB:                       0,
		DialTimeout:              5 * time.Second,
		ReadTimeout:              3 * time.Second,
		WriteTimeout:             3 * time.Second,
		PoolSize:                 10,
		IngestionLockTTL:         24 * time.Hour,
		PipelineGuardCategoryTTL: 60 * time.Second,
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
	if v := os.Getenv("REDIS_INGESTION_LOCK_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse REDIS_INGESTION_LOCK_TTL %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_INGESTION_LOCK_TTL %q: must be positive", v)
		}
		cfg.IngestionLockTTL = d
	}
	if v := os.Getenv("PIPELINE_GUARD_CATEGORY_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("parse PIPELINE_GUARD_CATEGORY_TTL %q: %w", v, err)
		}
		if d <= 0 {
			return RedisConfig{}, fmt.Errorf("invalid PIPELINE_GUARD_CATEGORY_TTL %q: must be positive", v)
		}
		cfg.PipelineGuardCategoryTTL = d
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
	CategoryInterval time.Duration // 카테고리 목록 폴링 주기 — SCHEDULER_CATEGORY_INTERVAL (default: 30m, 이슈 #329)
	JobTimeout       time.Duration // 개별 Job 최대 실행 시간 — SCHEDULER_JOB_TIMEOUT (default: 30s)
	MaxRetries       int           // Job 최대 재시도 횟수 — SCHEDULER_MAX_RETRIES (default: 3)

	// Backlog throttle: publish 직전 Kafka crawl 토픽의
	// consumer-group lag 가 임계값 초과 시 발행 차단.
	// MaxBacklog <= 0 → throttle 비활성 (기본).
	MaxBacklog          int64         // SCHEDULER_MAX_BACKLOG (default: 0 — disabled)
	BacklogCheckTimeout time.Duration // SCHEDULER_BACKLOG_CHECK_TIMEOUT (default: 5s)
}

// DefaultSchedulerConfig는 기본 SchedulerConfig를 반환합니다.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		// migration 020 (#329) 이 DB 의 news interval 을 30m 으로 단축한 것과 일관 —
		// DB 부재 환경에서 DefaultEntries fallback 도 동일 주기로 동작.
		CategoryInterval:    30 * time.Minute,
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

// PromptConfig 는 LLM prompt loader 의 wiring 설정입니다.
//
// pkg/llm/prompt 패키지의 NewDefaultLoader 가 본 설정으로 외부 파일 / embed fallback 정책을
// 결정. 도메인 패키지는 본 config 타입을 import 하지 않음 — cmd/* 가 LoadPrompt 결과의
// primitives (Dir, DirSet) 를 도메인 함수에 전달.
type PromptConfig struct {
	// Dir 은 LLM_PROMPT_DIR 환경변수 값 (DirSet=true 일 때만 의미 있음).
	// 빈 문자열 + DirSet=true → embed-only 강제 운영 의도.
	Dir string

	// DirSet 은 LLM_PROMPT_DIR 환경변수가 설정되었는지 여부 (값이 빈 문자열이라도 true).
	// os.Getenv 로는 unset 과 빈값 구분 불가하므로 명시 분리.
	DirSet bool
}

// LoadPrompt 는 .env 파일을 로드한 후 LLM_PROMPT_DIR 환경변수로 PromptConfig 를 구성합니다.
//
// 의미:
//   - 미설정      → DirSet=false   (cmd 가 DefaultDir auto-detection 시도)
//   - 빈 값 명시  → DirSet=true, Dir="" (embed-only 강제)
//   - 값 명시     → DirSet=true, Dir=값 (file → embed chain)
func LoadPrompt(envFiles ...string) (PromptConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return PromptConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	v, ok := os.LookupEnv("LLM_PROMPT_DIR")
	return PromptConfig{Dir: v, DirSet: ok}, nil
}

// GoogleCSEConfig 는 Google Custom Search Engine API client wiring 설정입니다.
//
// 환경변수:
//   - GOOGLE_CSE_API_KEY (필수, 빈 문자열이면 client 미생성 → search handler 비활성화)
//   - GOOGLE_CSE_CX      (필수, search engine ID)
//   - GOOGLE_CSE_TIMEOUT (default 10s)
//
// 운영 정책:
//   - APIKey 또는 CX 가 미설정이면 cmd 측 wiring 이 search handler 를 skip (warn 로그) — search
//     기능은 optional, 미설정 환경에서도 fetcher / parser / validate 흐름은 정상.
type GoogleCSEConfig struct {
	APIKey  string
	CX      string
	Timeout time.Duration
}

// DefaultGoogleCSEConfig 는 기본 GoogleCSEConfig 를 반환합니다 (Timeout 만 default).
func DefaultGoogleCSEConfig() GoogleCSEConfig {
	return GoogleCSEConfig{Timeout: 10 * time.Second}
}

// LoadGoogleCSE 는 .env 파일을 로드한 후 OS 환경변수로 GoogleCSEConfig 를 구성합니다.
//
// 빈 APIKey / CX 는 에러 아님 — 호출자가 IsConfigured 로 분기.
func LoadGoogleCSE(envFiles ...string) (GoogleCSEConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return GoogleCSEConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultGoogleCSEConfig()
	cfg.APIKey = os.Getenv("GOOGLE_CSE_API_KEY")
	cfg.CX = os.Getenv("GOOGLE_CSE_CX")

	if v := os.Getenv("GOOGLE_CSE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return GoogleCSEConfig{}, fmt.Errorf("parse GOOGLE_CSE_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return GoogleCSEConfig{}, fmt.Errorf("GOOGLE_CSE_TIMEOUT must be positive, got %s", d)
		}
		cfg.Timeout = d
	}

	return cfg, nil
}

// IsConfigured 는 APIKey + CX 가 모두 set 되었는지 여부를 반환합니다.
//
// false 면 cmd 가 search handler 를 wire 하지 않음 — search 기능 optional 정책.
func (c GoogleCSEConfig) IsConfigured() bool {
	return c.APIKey != "" && c.CX != ""
}
