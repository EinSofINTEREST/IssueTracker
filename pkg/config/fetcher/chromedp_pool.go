package fetchercfg

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

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
