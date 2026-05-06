package llmgen

import (
	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/storage"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/redis"
)

// Build 는 buildLLMProvider 결과로 Generator 를 구성합니다 (이슈 #149).
//
// provider 가 nil (LLM 비활성) 이면 nil 반환 — parser worker 는 ErrNoRule 시 raw 만 잔존.
// New 자체 실패는 wiring 버그라 fatal — 도달하면 dependency injection 모순 (이슈 #208).
// redisClient 가 nil 이면 in-process memInflightLocker 로 graceful degrade (이슈 #261).
//
// 본 함수는 cmd/* 바이너리의 wiring 헬퍼 — main.go 에서 도메인 logic 분리 (이슈 #276).
func Build(provider llm.Provider, repo storage.ParsingRuleRepository, resolver *rule.Resolver, redisClient *redis.Client, log *logger.Logger) *Generator {
	if provider == nil {
		return nil
	}
	gen, err := New(provider, repo, resolver, log)
	if err != nil {
		log.WithError(err).Fatal("failed to construct llmgen generator")
	}
	if redisClient != nil {
		gen.SetLocker(NewRedisInflightLocker(redisClient.Raw(), DefaultInflightLockTTL))
		log.Info("llmgen: Redis 분산 inflight lock 활성화")
	}
	return gen
}
