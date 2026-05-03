package config_test

import (
	"testing"
	"time"

	"issuetracker/pkg/config"
)

func TestLoad_DefaultValues(t *testing.T) {
	// TestLoad_DefaultValues는 환경변수가 없는 경우 기본값이 반환되는지 검증합니다.
	unsetEnvVars(t)

	cfg, err := config.Load("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("기본값 로드 실패: %v", err)
	}

	def := config.DefaultDBConfig()
	if cfg.Host != def.Host {
		t.Errorf("Host: got %q, want %q", cfg.Host, def.Host)
	}
	if cfg.Port != def.Port {
		t.Errorf("Port: got %d, want %d", cfg.Port, def.Port)
	}
	if cfg.MaxConns != def.MaxConns {
		t.Errorf("MaxConns: got %d, want %d", cfg.MaxConns, def.MaxConns)
	}
	if cfg.ConnTimeout != def.ConnTimeout {
		t.Errorf("ConnTimeout: got %v, want %v", cfg.ConnTimeout, def.ConnTimeout)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	// TestLoad_EnvOverride는 환경변수로 기본값을 덮어쓸 수 있는지 검증합니다.
	unsetEnvVars(t)
	t.Setenv("POSTGRES_HOST", "db.example.com")
	t.Setenv("POSTGRES_PORT", "5433")
	t.Setenv("POSTGRES_USER", "admin")
	t.Setenv("POSTGRES_MAX_CONNS", "50")
	t.Setenv("POSTGRES_CONN_TIMEOUT", "10s")

	cfg, err := config.Load("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("환경변수 로드 실패: %v", err)
	}

	if cfg.Host != "db.example.com" {
		t.Errorf("Host: got %q, want %q", cfg.Host, "db.example.com")
	}
	if cfg.Port != 5433 {
		t.Errorf("Port: got %d, want 5433", cfg.Port)
	}
	if cfg.User != "admin" {
		t.Errorf("User: got %q, want %q", cfg.User, "admin")
	}
	if cfg.MaxConns != 50 {
		t.Errorf("MaxConns: got %d, want 50", cfg.MaxConns)
	}
	if cfg.ConnTimeout != 10*time.Second {
		t.Errorf("ConnTimeout: got %v, want 10s", cfg.ConnTimeout)
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	unsetEnvVars(t)
	t.Setenv("POSTGRES_PORT", "not-a-number")

	_, err := config.Load("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("잘못된 POSTGRES_PORT 값에 대해 에러가 반환되어야 합니다")
	}
}

func TestLoad_InvalidMaxConns(t *testing.T) {
	unsetEnvVars(t)
	t.Setenv("POSTGRES_MAX_CONNS", "bad")

	_, err := config.Load("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("잘못된 POSTGRES_MAX_CONNS 값에 대해 에러가 반환되어야 합니다")
	}
}

func TestLoad_InvalidMinConns(t *testing.T) {
	unsetEnvVars(t)
	t.Setenv("POSTGRES_MIN_CONNS", "bad")

	_, err := config.Load("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("잘못된 POSTGRES_MIN_CONNS 값에 대해 에러가 반환되어야 합니다")
	}
}

func TestLoad_InvalidConnTimeout(t *testing.T) {
	unsetEnvVars(t)
	t.Setenv("POSTGRES_CONN_TIMEOUT", "not-a-duration")

	_, err := config.Load("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("잘못된 POSTGRES_CONN_TIMEOUT 값에 대해 에러가 반환되어야 합니다")
	}
}

func TestLoad_MissingEnvFileIsIgnored(t *testing.T) {
	// TestLoad_MissingEnvFileIsIgnored는 .env 파일이 없는 경우 에러 없이 기본값으로 로드되는지 검증합니다.
	unsetEnvVars(t)

	_, err := config.Load("/tmp/does-not-exist.env")
	if err != nil {
		t.Fatalf("존재하지 않는 .env 파일은 에러를 반환하면 안 됩니다: %v", err)
	}
}

// unsetLogEnvVars는 테스트 격리를 위해 LOG_* 환경변수를 초기화합니다.
func unsetLogEnvVars(t *testing.T) {
	t.Helper()
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_PRETTY", "")
}

func TestLoadLog_DefaultValues(t *testing.T) {
	unsetLogEnvVars(t)

	cfg, err := config.LoadLog("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("기본값 로드 실패: %v", err)
	}

	def := config.DefaultLogConfig()
	if cfg.Level != def.Level {
		t.Errorf("Level: got %q, want %q", cfg.Level, def.Level)
	}
	if cfg.Pretty != def.Pretty {
		t.Errorf("Pretty: got %v, want %v", cfg.Pretty, def.Pretty)
	}
}

func TestLoadLog_EnvOverride(t *testing.T) {
	tests := []struct {
		name       string
		level      string
		pretty     string
		wantLevel  string
		wantPretty bool
	}{
		{"debug level", "debug", "false", "debug", false},
		{"warn level", "warn", "false", "warn", false},
		{"error level", "error", "false", "error", false},
		{"pretty true", "info", "true", "info", true},
		{"pretty false", "info", "false", "info", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetLogEnvVars(t)
			t.Setenv("LOG_LEVEL", tt.level)
			t.Setenv("LOG_PRETTY", tt.pretty)

			cfg, err := config.LoadLog("/tmp/nonexistent-env-file.env")
			if err != nil {
				t.Fatalf("환경변수 로드 실패: %v", err)
			}
			if cfg.Level != tt.wantLevel {
				t.Errorf("Level: got %q, want %q", cfg.Level, tt.wantLevel)
			}
			if cfg.Pretty != tt.wantPretty {
				t.Errorf("Pretty: got %v, want %v", cfg.Pretty, tt.wantPretty)
			}
		})
	}
}

func TestLoadLog_InvalidLevel(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"uppercase", "INFO"},
		{"mixed case", "Debug"},
		{"unknown level", "verbose"},
		{"empty-like typo", "infoo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetLogEnvVars(t)
			t.Setenv("LOG_LEVEL", tt.value)

			_, err := config.LoadLog("/tmp/nonexistent-env-file.env")
			if err == nil {
				t.Fatalf("LOG_LEVEL=%q: 에러가 반환되어야 합니다", tt.value)
			}
		})
	}
}

func TestLoadLog_InvalidPretty(t *testing.T) {
	unsetLogEnvVars(t)
	t.Setenv("LOG_PRETTY", "not-a-bool")

	_, err := config.LoadLog("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("잘못된 LOG_PRETTY 값에 대해 에러가 반환되어야 합니다")
	}
}

func TestLoadLog_MissingEnvFileIsIgnored(t *testing.T) {
	unsetLogEnvVars(t)

	_, err := config.LoadLog("/tmp/does-not-exist.env")
	if err != nil {
		t.Fatalf("존재하지 않는 .env 파일은 에러를 반환하면 안 됩니다: %v", err)
	}
}

// unsetEnvVars는 테스트 격리를 위해 POSTGRES_* 환경변수를 초기화합니다.
func unsetEnvVars(t *testing.T) {
	t.Helper()
	vars := []string{
		"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_USER",
		"POSTGRES_PASSWORD", "POSTGRES_DB", "POSTGRES_SSLMODE",
		"POSTGRES_MAX_CONNS", "POSTGRES_MIN_CONNS", "POSTGRES_CONN_TIMEOUT",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

// unsetRedisEnvVars는 테스트 격리를 위해 REDIS_* 환경변수를 초기화합니다.
func unsetRedisEnvVars(t *testing.T) {
	t.Helper()
	vars := []string{
		"REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD",
		"REDIS_DB", "REDIS_DIAL_TIMEOUT", "REDIS_READ_TIMEOUT",
		"REDIS_WRITE_TIMEOUT", "REDIS_POOL_SIZE",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

func TestLoadRedis_DefaultValues(t *testing.T) {
	unsetRedisEnvVars(t)

	cfg, err := config.LoadRedis("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("기본값 로드 실패: %v", err)
	}

	def := config.DefaultRedisConfig()
	if cfg.Host != def.Host {
		t.Errorf("Host: got %q, want %q", cfg.Host, def.Host)
	}
	if cfg.Port != def.Port {
		t.Errorf("Port: got %d, want %d", cfg.Port, def.Port)
	}
	if cfg.DB != def.DB {
		t.Errorf("DB: got %d, want %d", cfg.DB, def.DB)
	}
	if cfg.PoolSize != def.PoolSize {
		t.Errorf("PoolSize: got %d, want %d", cfg.PoolSize, def.PoolSize)
	}
	if cfg.DialTimeout != def.DialTimeout {
		t.Errorf("DialTimeout: got %v, want %v", cfg.DialTimeout, def.DialTimeout)
	}
}

func TestLoadRedis_EnvOverride(t *testing.T) {
	unsetRedisEnvVars(t)
	t.Setenv("REDIS_HOST", "redis.example.com")
	t.Setenv("REDIS_PORT", "6380")
	t.Setenv("REDIS_DB", "2")
	t.Setenv("REDIS_POOL_SIZE", "20")
	t.Setenv("REDIS_DIAL_TIMEOUT", "10s")

	cfg, err := config.LoadRedis("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("환경변수 로드 실패: %v", err)
	}

	if cfg.Host != "redis.example.com" {
		t.Errorf("Host: got %q, want %q", cfg.Host, "redis.example.com")
	}
	if cfg.Port != 6380 {
		t.Errorf("Port: got %d, want 6380", cfg.Port)
	}
	if cfg.DB != 2 {
		t.Errorf("DB: got %d, want 2", cfg.DB)
	}
	if cfg.PoolSize != 20 {
		t.Errorf("PoolSize: got %d, want 20", cfg.PoolSize)
	}
	if cfg.DialTimeout != 10*time.Second {
		t.Errorf("DialTimeout: got %v, want 10s", cfg.DialTimeout)
	}
}

func TestLoadRedis_InvalidPort(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"not a number", "not-a-number"},
		{"port zero", "0"},
		{"port too large", "70000"},
		{"negative port", "-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetRedisEnvVars(t)
			t.Setenv("REDIS_PORT", tt.value)

			_, err := config.LoadRedis("/tmp/nonexistent-env-file.env")
			if err == nil {
				t.Fatalf("REDIS_PORT=%q: 에러가 반환되어야 합니다", tt.value)
			}
		})
	}
}

func TestLoadRedis_InvalidDB(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"not a number", "not-a-number"},
		{"negative db index", "-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetRedisEnvVars(t)
			t.Setenv("REDIS_DB", tt.value)

			_, err := config.LoadRedis("/tmp/nonexistent-env-file.env")
			if err == nil {
				t.Fatalf("REDIS_DB=%q: 에러가 반환되어야 합니다", tt.value)
			}
		})
	}
}

func TestLoadRedis_InvalidPoolSize(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"not a number", "not-a-number"},
		{"zero", "0"},
		{"negative", "-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetRedisEnvVars(t)
			t.Setenv("REDIS_POOL_SIZE", tt.value)

			_, err := config.LoadRedis("/tmp/nonexistent-env-file.env")
			if err == nil {
				t.Fatalf("REDIS_POOL_SIZE=%q: 에러가 반환되어야 합니다", tt.value)
			}
		})
	}
}

func TestLoadRedis_InvalidTimeouts(t *testing.T) {
	timeoutEnvVars := []string{
		"REDIS_DIAL_TIMEOUT",
		"REDIS_READ_TIMEOUT",
		"REDIS_WRITE_TIMEOUT",
	}
	invalidValues := []struct {
		name  string
		value string
	}{
		{"not a duration", "not-a-duration"},
		{"zero", "0s"},
		{"negative", "-1s"},
	}

	for _, envVar := range timeoutEnvVars {
		for _, tt := range invalidValues {
			t.Run(envVar+"/"+tt.name, func(t *testing.T) {
				unsetRedisEnvVars(t)
				t.Setenv(envVar, tt.value)

				_, err := config.LoadRedis("/tmp/nonexistent-env-file.env")
				if err == nil {
					t.Fatalf("%s=%q: 에러가 반환되어야 합니다", envVar, tt.value)
				}
			})
		}
	}
}

// unsetSchedulerEnvVars는 테스트 격리를 위해 SCHEDULER_* 환경변수를 초기화합니다.
func unsetSchedulerEnvVars(t *testing.T) {
	t.Helper()
	vars := []string{
		"SCHEDULER_CATEGORY_INTERVAL",
		"SCHEDULER_JOB_TIMEOUT",
		"SCHEDULER_MAX_RETRIES",
		"SCHEDULER_MAX_BACKLOG",
		"SCHEDULER_BACKLOG_CHECK_TIMEOUT",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

func TestLoadScheduler_DefaultValues(t *testing.T) {
	unsetSchedulerEnvVars(t)

	cfg, err := config.LoadScheduler("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("기본값 로드 실패: %v", err)
	}

	def := config.DefaultSchedulerConfig()
	if cfg.MaxBacklog != def.MaxBacklog {
		t.Errorf("MaxBacklog: got %d, want %d (disabled)", cfg.MaxBacklog, def.MaxBacklog)
	}
	if cfg.BacklogCheckTimeout != def.BacklogCheckTimeout {
		t.Errorf("BacklogCheckTimeout: got %v, want %v", cfg.BacklogCheckTimeout, def.BacklogCheckTimeout)
	}
}

func TestLoadScheduler_BacklogEnvOverride(t *testing.T) {
	unsetSchedulerEnvVars(t)
	t.Setenv("SCHEDULER_MAX_BACKLOG", "10000")
	t.Setenv("SCHEDULER_BACKLOG_CHECK_TIMEOUT", "2s")

	cfg, err := config.LoadScheduler("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("환경변수 로드 실패: %v", err)
	}

	if cfg.MaxBacklog != 10000 {
		t.Errorf("MaxBacklog: got %d, want 10000", cfg.MaxBacklog)
	}
	if cfg.BacklogCheckTimeout != 2*time.Second {
		t.Errorf("BacklogCheckTimeout: got %v, want 2s", cfg.BacklogCheckTimeout)
	}
}

func TestLoadScheduler_InvalidMaxBacklog(t *testing.T) {
	unsetSchedulerEnvVars(t)
	t.Setenv("SCHEDULER_MAX_BACKLOG", "not-a-number")

	_, err := config.LoadScheduler("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("SCHEDULER_MAX_BACKLOG 가 정수가 아니면 에러가 반환되어야 합니다")
	}
}

func TestLoadScheduler_InvalidBacklogCheckTimeout(t *testing.T) {
	unsetSchedulerEnvVars(t)
	t.Setenv("SCHEDULER_BACKLOG_CHECK_TIMEOUT", "not-a-duration")

	_, err := config.LoadScheduler("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("SCHEDULER_BACKLOG_CHECK_TIMEOUT 가 duration 형식이 아니면 에러가 반환되어야 합니다")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LoadPathInfer (이슈 #173 단계 2)
// ─────────────────────────────────────────────────────────────────────────────

func TestLoadPathInfer_DefaultValues(t *testing.T) {
	t.Setenv("PATHINFER_MIN_SAMPLES", "")

	cfg, err := config.LoadPathInfer("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("기본값 로드 실패: %v", err)
	}
	if cfg.MinSamples != 3 {
		t.Errorf("MinSamples default: got %d, want 3", cfg.MinSamples)
	}
}

func TestLoadPathInfer_OverrideViaEnv(t *testing.T) {
	t.Setenv("PATHINFER_MIN_SAMPLES", "5")

	cfg, err := config.LoadPathInfer("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("override 로드 실패: %v", err)
	}
	if cfg.MinSamples != 5 {
		t.Errorf("MinSamples override: got %d, want 5", cfg.MinSamples)
	}
}

func TestLoadPathInfer_InvalidNonInteger(t *testing.T) {
	t.Setenv("PATHINFER_MIN_SAMPLES", "abc")

	_, err := config.LoadPathInfer("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("PATHINFER_MIN_SAMPLES 가 정수 아니면 에러")
	}
}

func TestLoadPathInfer_InvalidZeroOrNegative(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("PATHINFER_MIN_SAMPLES", v)
			_, err := config.LoadPathInfer("/tmp/nonexistent-env-file.env")
			if err == nil {
				t.Fatalf("PATHINFER_MIN_SAMPLES=%q 는 1 이상이어야 함", v)
			}
		})
	}
}

// 이슈 #229 — chromedp pool config 테스트.
// SemaphoreCapacity 의미가 글로벌 → per-worker 로 변경되었으며 default 가 4 → 2 로 조정됨.

func unsetFetcherChromedpPoolEnvVars(t *testing.T) {
	t.Helper()
	vars := []string{
		"FETCHER_CHROMEDP_POOL_ENABLED",
		"FETCHER_CHROMEDP_WORKER_COUNT",
		"FETCHER_CHROMEDP_SEMAPHORE_CAPACITY",
		"FETCHER_CHROMEDP_REMOTE_URLS",
		"FETCHER_CHROMEDP_REMOTE_URL_PATTERN",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

func TestLoadFetcherChromedpPool_DefaultValues(t *testing.T) {
	unsetFetcherChromedpPoolEnvVars(t)

	cfg, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("기본값 로드 실패: %v", err)
	}

	def := config.DefaultFetcherChromedpPoolConfig()
	if cfg.Enabled != def.Enabled {
		t.Errorf("Enabled: got %v, want %v", cfg.Enabled, def.Enabled)
	}
	if cfg.WorkerCount != def.WorkerCount {
		t.Errorf("WorkerCount: got %d, want %d", cfg.WorkerCount, def.WorkerCount)
	}
	// 이슈 #229 — per-worker 의미 + KafkaConsumerPool 순차 처리 모델 정정 (gemini 피드백):
	// capacity > 1 은 추가 동시성 이득 없으므로 default 1.
	if def.SemaphoreCapacity != 1 {
		t.Errorf("DefaultFetcherChromedpPoolConfig.SemaphoreCapacity: got %d, want 1 (per-worker, 1 이상 의미 없음)", def.SemaphoreCapacity)
	}
	if cfg.SemaphoreCapacity != def.SemaphoreCapacity {
		t.Errorf("SemaphoreCapacity: got %d, want %d", cfg.SemaphoreCapacity, def.SemaphoreCapacity)
	}

	// 이슈 #230 — RemoteURLs default: ws://localhost:9222 × WorkerCount (단일 Chrome 호환).
	if len(cfg.RemoteURLs) != cfg.WorkerCount {
		t.Errorf("RemoteURLs length: got %d, want %d (= WorkerCount)", len(cfg.RemoteURLs), cfg.WorkerCount)
	}
	for i, url := range cfg.RemoteURLs {
		if url != "ws://localhost:9222" {
			t.Errorf("RemoteURLs[%d]: got %q, want ws://localhost:9222 (default)", i, url)
		}
	}
}

func TestLoadFetcherChromedpPool_RemoteURLsEnvOverride(t *testing.T) {
	unsetFetcherChromedpPoolEnvVars(t)
	t.Setenv("FETCHER_CHROMEDP_WORKER_COUNT", "3")
	t.Setenv("FETCHER_CHROMEDP_REMOTE_URLS", "ws://chrome-1:9222, ws://chrome-2:9222 ,ws://chrome-3:9222")

	cfg, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("RemoteURLs override 실패: %v", err)
	}

	want := []string{"ws://chrome-1:9222", "ws://chrome-2:9222", "ws://chrome-3:9222"}
	if len(cfg.RemoteURLs) != len(want) {
		t.Fatalf("RemoteURLs length: got %d, want %d", len(cfg.RemoteURLs), len(want))
	}
	for i, w := range want {
		if cfg.RemoteURLs[i] != w {
			t.Errorf("RemoteURLs[%d]: got %q, want %q (TrimSpace 적용 검증)", i, cfg.RemoteURLs[i], w)
		}
	}
}

func TestLoadFetcherChromedpPool_RemoteURLsLengthMismatch_Fails(t *testing.T) {
	// 이슈 #230 — RemoteURLs 길이가 WorkerCount 와 일치하지 않으면 fail-fast.
	cases := []struct {
		name        string
		workerCount string
		urls        string
	}{
		{"too few", "3", "ws://chrome-1:9222,ws://chrome-2:9222"},
		{"too many", "1", "ws://chrome-1:9222,ws://chrome-2:9222"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unsetFetcherChromedpPoolEnvVars(t)
			t.Setenv("FETCHER_CHROMEDP_WORKER_COUNT", c.workerCount)
			t.Setenv("FETCHER_CHROMEDP_REMOTE_URLS", c.urls)
			_, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
			if err == nil {
				t.Fatalf("WorkerCount=%s, urls=%q 는 거부되어야 함", c.workerCount, c.urls)
			}
		})
	}
}

func TestLoadFetcherChromedpPool_RemoteURLsEmptyEntry_Fails(t *testing.T) {
	// 이슈 #230 — 콤마 구분 list 안 빈 항목 (예: "url1,,url2") 거부.
	unsetFetcherChromedpPoolEnvVars(t)
	t.Setenv("FETCHER_CHROMEDP_WORKER_COUNT", "2")
	t.Setenv("FETCHER_CHROMEDP_REMOTE_URLS", "ws://chrome-1:9222, ,ws://chrome-2:9222")
	_, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("빈 항목 포함 list 는 거부되어야 함")
	}
}

func TestLoadFetcherChromedpPool_RemoteURLPattern_Expands(t *testing.T) {
	// 이슈 #230 — PATTERN 의 {n} placeholder 가 1..WorkerCount 로 치환되는지 검증.
	unsetFetcherChromedpPoolEnvVars(t)
	t.Setenv("FETCHER_CHROMEDP_WORKER_COUNT", "3")
	t.Setenv("FETCHER_CHROMEDP_REMOTE_URL_PATTERN", "ws://chrome-{n}:9222")

	cfg, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("PATTERN expand 실패: %v", err)
	}

	want := []string{"ws://chrome-1:9222", "ws://chrome-2:9222", "ws://chrome-3:9222"}
	if len(cfg.RemoteURLs) != len(want) {
		t.Fatalf("RemoteURLs length: got %d, want %d", len(cfg.RemoteURLs), len(want))
	}
	for i, w := range want {
		if cfg.RemoteURLs[i] != w {
			t.Errorf("RemoteURLs[%d]: got %q, want %q ({n} 치환 1-indexed 검증)", i, cfg.RemoteURLs[i], w)
		}
	}
}

func TestLoadFetcherChromedpPool_RemoteURLsOverridesPattern(t *testing.T) {
	// 이슈 #230 — REMOTE_URLS 가 PATTERN 보다 우선 적용되는지 검증.
	unsetFetcherChromedpPoolEnvVars(t)
	t.Setenv("FETCHER_CHROMEDP_WORKER_COUNT", "2")
	t.Setenv("FETCHER_CHROMEDP_REMOTE_URLS", "ws://explicit-1:9222,ws://explicit-2:9222")
	t.Setenv("FETCHER_CHROMEDP_REMOTE_URL_PATTERN", "ws://chrome-{n}:9222")

	cfg, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("override 실패: %v", err)
	}

	if cfg.RemoteURLs[0] != "ws://explicit-1:9222" || cfg.RemoteURLs[1] != "ws://explicit-2:9222" {
		t.Errorf("RemoteURLs: got %v, want explicit URLs (PATTERN 보다 우선)", cfg.RemoteURLs)
	}
}

func TestLoadFetcherChromedpPool_RemoteURLPattern_MissingPlaceholder_Fails(t *testing.T) {
	// 이슈 #230 — PATTERN 에 {n} 누락 시 모든 worker 가 같은 url 로 향함 → 1:1 매핑 무력화.
	// fail-fast 로 운영자가 명시적 의사결정 강제.
	unsetFetcherChromedpPoolEnvVars(t)
	t.Setenv("FETCHER_CHROMEDP_WORKER_COUNT", "2")
	t.Setenv("FETCHER_CHROMEDP_REMOTE_URL_PATTERN", "ws://chrome-static:9222")

	_, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
	if err == nil {
		t.Fatal("{n} placeholder 누락 PATTERN 은 거부되어야 함")
	}
}

func TestLoadFetcherChromedpPool_EnvOverride(t *testing.T) {
	unsetFetcherChromedpPoolEnvVars(t)
	t.Setenv("FETCHER_CHROMEDP_POOL_ENABLED", "true")
	t.Setenv("FETCHER_CHROMEDP_WORKER_COUNT", "4")
	t.Setenv("FETCHER_CHROMEDP_SEMAPHORE_CAPACITY", "3")

	cfg, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
	if err != nil {
		t.Fatalf("환경변수 override 실패: %v", err)
	}
	if !cfg.Enabled {
		t.Errorf("Enabled: got false, want true")
	}
	if cfg.WorkerCount != 4 {
		t.Errorf("WorkerCount: got %d, want 4", cfg.WorkerCount)
	}
	if cfg.SemaphoreCapacity != 3 {
		t.Errorf("SemaphoreCapacity: got %d, want 3", cfg.SemaphoreCapacity)
	}
}

func TestLoadFetcherChromedpPool_InvalidWorkerCount(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc"} {
		t.Run(v, func(t *testing.T) {
			unsetFetcherChromedpPoolEnvVars(t)
			t.Setenv("FETCHER_CHROMEDP_WORKER_COUNT", v)
			_, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
			if err == nil {
				t.Fatalf("FETCHER_CHROMEDP_WORKER_COUNT=%q 는 거부되어야 함", v)
			}
		})
	}
}

func TestLoadFetcherChromedpPool_InvalidSemaphoreCapacity(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc"} {
		t.Run(v, func(t *testing.T) {
			unsetFetcherChromedpPoolEnvVars(t)
			t.Setenv("FETCHER_CHROMEDP_SEMAPHORE_CAPACITY", v)
			_, err := config.LoadFetcherChromedpPool("/tmp/nonexistent-env-file.env")
			if err == nil {
				t.Fatalf("FETCHER_CHROMEDP_SEMAPHORE_CAPACITY=%q 는 거부되어야 함", v)
			}
		})
	}
}
