package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	logger "issuetracker/pkg/logger"
)

func TestNew(t *testing.T) {
	cfg := logger.DefaultConfig()
	log := logger.New(cfg)

	assert.NotNil(t, log)
}

func TestDefaultConfig(t *testing.T) {
	cfg := logger.DefaultConfig()

	assert.Equal(t, logger.LevelInfo, cfg.Level)
	assert.False(t, cfg.Pretty)
	assert.NotNil(t, cfg.Output)
}

func TestLogger_WithField(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelDebug

	log := logger.New(cfg)
	log = log.WithField("key", "value")
	log.Debug("test message")

	output := buf.String()
	assert.Contains(t, output, "test message")
	assert.Contains(t, output, "key")
	assert.Contains(t, output, "value")
}

func TestLogger_WithFields(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelDebug

	log := logger.New(cfg)
	log = log.WithFields(map[string]interface{}{
		"key1": "value1",
		"key2": 123,
	})
	log.Debug("test message")

	output := buf.String()
	assert.Contains(t, output, "test message")
	assert.Contains(t, output, "key1")
	assert.Contains(t, output, "value1")
	assert.Contains(t, output, "key2")
}

func TestLogger_WithError(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelError

	log := logger.New(cfg)
	testErr := assert.AnError
	log = log.WithError(testErr)
	log.Error("test error message")

	output := buf.String()
	assert.Contains(t, output, "test error message")
	assert.Contains(t, output, "error")
}

func TestLogger_Levels(t *testing.T) {
	tests := []struct {
		name      string
		level     logger.Level
		logFunc   func(*logger.Logger)
		shouldLog bool
	}{
		{
			name:  "debug level logs debug",
			level: logger.LevelDebug,
			logFunc: func(l *logger.Logger) {
				l.Debug("debug message")
			},
			shouldLog: true,
		},
		{
			name:  "info level does not log debug",
			level: logger.LevelInfo,
			logFunc: func(l *logger.Logger) {
				l.Debug("debug message")
			},
			shouldLog: false,
		},
		{
			name:  "info level logs info",
			level: logger.LevelInfo,
			logFunc: func(l *logger.Logger) {
				l.Info("info message")
			},
			shouldLog: true,
		},
		{
			name:  "warn level logs warn",
			level: logger.LevelWarn,
			logFunc: func(l *logger.Logger) {
				l.Warn("warn message")
			},
			shouldLog: true,
		},
		{
			name:  "error level logs error",
			level: logger.LevelError,
			logFunc: func(l *logger.Logger) {
				l.Error("error message")
			},
			shouldLog: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			cfg := logger.DefaultConfig()
			cfg.Output = buf
			cfg.Level = tt.level

			log := logger.New(cfg)
			tt.logFunc(log)

			output := buf.String()
			if tt.shouldLog {
				assert.NotEmpty(t, output, "expected log output")
			} else {
				assert.Empty(t, output, "expected no log output")
			}
		})
	}
}

func TestLogger_Debugf(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelDebug

	log := logger.New(cfg)
	log.Debugf("formatted %s %d", "message", 123)

	output := buf.String()
	assert.Contains(t, output, "formatted message 123")
}

func TestLogger_Infof(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelInfo

	log := logger.New(cfg)
	log.Infof("formatted %s %d", "message", 456)

	output := buf.String()
	assert.Contains(t, output, "formatted message 456")
}

func TestLogger_Warnf(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelWarn

	log := logger.New(cfg)
	log.Warnf("formatted %s %d", "warning", 789)

	output := buf.String()
	assert.Contains(t, output, "formatted warning 789")
}

func TestLogger_Errorf(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelError

	log := logger.New(cfg)
	log.Errorf("formatted %s %d", "error", 101)

	output := buf.String()
	assert.Contains(t, output, "formatted error 101")
}

func TestLogger_ToContext_FromContext(t *testing.T) {
	cfg := logger.DefaultConfig()
	log := logger.New(cfg)

	ctx := context.Background()
	ctx = log.ToContext(ctx)

	retrieved := logger.FromContext(ctx)
	assert.NotNil(t, retrieved)
}

func TestLogger_FromContext_NoLogger(t *testing.T) {
	ctx := context.Background()
	log := logger.FromContext(ctx)

	// Should return default logger
	assert.NotNil(t, log)
}

func TestLogger_WithRequestID(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelInfo

	log := logger.New(cfg)
	log = log.WithRequestID("req-123")
	log.Info("test message")

	output := buf.String()
	assert.Contains(t, output, "req-123")
	assert.Contains(t, output, "request_id")
}

func TestLogger_WithCrawler(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelInfo

	log := logger.New(cfg)
	log = log.WithCrawler("cnn", "news", "US")
	log.Info("test message")

	output := buf.String()
	assert.Contains(t, output, "cnn")
	assert.Contains(t, output, "news")
	assert.Contains(t, output, "US")
}

func TestLogger_JSONFormat(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelInfo
	cfg.Pretty = false

	log := logger.New(cfg)
	log.WithField("test_key", "test_value").Info("json test")

	output := buf.String()

	// JSON 파싱 가능한지 확인
	var logEntry map[string]interface{}
	err := json.Unmarshal([]byte(strings.TrimSpace(output)), &logEntry)
	require.NoError(t, err)

	assert.Equal(t, "json test", logEntry["message"])
	assert.Equal(t, "test_value", logEntry["test_key"])
	assert.Equal(t, "info", logEntry["level"])
}

func TestLogger_PrettyFormat(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelInfo
	cfg.Pretty = true

	log := logger.New(cfg)
	log.Info("pretty test")

	output := buf.String()
	// Pretty format should be human-readable
	assert.Contains(t, output, "pretty test")
	assert.Contains(t, output, "INF")
}
