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
