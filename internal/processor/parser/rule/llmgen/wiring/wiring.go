// Package wiring 은 cmd/* 바이너리가 환경 의존성 (config / Redis 등) 을 주입하여 llmgen.Generator 를
// 구성하는 헬퍼를 제공합니다.
//
// llmgen 도메인 패키지는 인프라 설정 (config / Redis raw client 등) 에 의존하지 않으며, 본 wiring
// 서브패키지가 그 결합을 흡수 — 도메인 로직과 인프라 wiring 의 분리.
package wiring

import (
	"fmt"

	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage"
	redisstore "issuetracker/internal/storage/redis"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/redis"
)

// Build 는 LLM provider + repository + resolver 로 llmgen.Generator 를 구성합니다.
//
// provider 는 호출자가 직접 주입 — 일반적으로 pkg/llm/wiring.BuildProvider 결과를 그대로 전달.
// provider 가 nil (LLM 비활성) 이면 (nil, nil) 반환 — parser worker 는 ErrNoRule 시 raw 만 잔존.
// llmgen.New 자체 실패는 wiring 버그 — 호출자 (main) 에서 Fatal 결정.
// redisClient 가 nil 이면 in-process memInflightLocker 로 graceful degrade.
func Build(provider llm.Provider, repo storage.ParsingRuleRepository, resolver *rule.Resolver, redisClient *redis.Client, log *logger.Logger) (*llmgen.Generator, error) {
	if provider == nil {
		return nil, nil
	}
	gen, err := llmgen.New(provider, repo, resolver, log)
	if err != nil {
		return nil, fmt.Errorf("construct llmgen generator: %w", err)
	}
	if redisClient != nil {
		locker, lockerErr := redisstore.NewInflightLocker(redisClient.Raw(), redisstore.DefaultInflightLockTTL)
		if lockerErr != nil {
			return nil, fmt.Errorf("construct redis inflight locker: %w", lockerErr)
		}
		gen.SetLocker(locker)
		log.Info("llmgen: Redis 분산 inflight lock 활성화")
	}
	return gen, nil
}
