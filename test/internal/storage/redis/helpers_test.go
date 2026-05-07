package redisstore_test

import "issuetracker/pkg/logger"

// newTestLogger 는 통합 테스트용 logger (default config).
func newTestLogger() *logger.Logger {
	return logger.New(logger.DefaultConfig())
}
