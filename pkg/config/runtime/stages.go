package runtimecfg

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"issuetracker/pkg/config/internal/parse"
)

// StagesConfig 는 cmd/issuetracker 가 한 프로세스 안에서 어느 단계 워커를 기동할지
// 제어합니다 (이슈 #443).
//
// 모든 stage 가 default true — env 미설정 시 기존 동작 (모든 stage 동시 기동) 그대로 보존.
// 운영자는 stage 별로 false 를 줘서 같은 바이너리를 fetcher-only / parser-only /
// validate-only / scheduler-only 노드로 배포할 수 있음. Kafka consumer group 이
// partition 분배를 처리하므로 stage 간 결합은 토픽 경유로 유지됨.
//
// 모든 stage 가 false 면 의미가 없으므로 LoadStages 가 명시적 에러를 반환.
type StagesConfig struct {
	// FetcherEnabled: fetcher worker pool (high/normal/low + chromedp) 기동 여부.
	// 환경변수 STAGES_FETCHER_ENABLED (default true).
	FetcherEnabled bool

	// ParserEnabled: parser worker (issuetracker.fetched consumer) 기동 여부.
	// 환경변수 STAGES_PARSER_ENABLED (default true).
	ParserEnabled bool

	// ValidateEnabled: validate worker (issuetracker.normalized consumer) 기동 여부.
	// 환경변수 STAGES_VALIDATE_ENABLED (default true).
	ValidateEnabled bool

	// SchedulerEnabled: scheduler (crawl job emitter) 기동 여부.
	// 환경변수 STAGES_SCHEDULER_ENABLED (default true).
	SchedulerEnabled bool
}

// DefaultStagesConfig 는 모든 stage 활성화된 상태를 반환합니다 (backward compat).
func DefaultStagesConfig() StagesConfig {
	return StagesConfig{
		FetcherEnabled:   true,
		ParserEnabled:    true,
		ValidateEnabled:  true,
		SchedulerEnabled: true,
	}
}

// LoadStages 는 .env 를 로드한 후 환경변수로 StagesConfig 를 구성합니다.
//
// 모든 stage 가 false 인 구성은 의미 없음 — LoadStages 가 에러로 거부.
func LoadStages(envFiles ...string) (StagesConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return StagesConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultStagesConfig()

	for _, op := range []error{
		parse.Bool("STAGES_FETCHER_ENABLED", &cfg.FetcherEnabled),
		parse.Bool("STAGES_PARSER_ENABLED", &cfg.ParserEnabled),
		parse.Bool("STAGES_VALIDATE_ENABLED", &cfg.ValidateEnabled),
		parse.Bool("STAGES_SCHEDULER_ENABLED", &cfg.SchedulerEnabled),
	} {
		if op != nil {
			return StagesConfig{}, op
		}
	}

	if !cfg.FetcherEnabled && !cfg.ParserEnabled && !cfg.ValidateEnabled && !cfg.SchedulerEnabled {
		return StagesConfig{}, fmt.Errorf("all stages disabled — at least one of STAGES_FETCHER_ENABLED/STAGES_PARSER_ENABLED/STAGES_VALIDATE_ENABLED/STAGES_SCHEDULER_ENABLED must be true")
	}

	return cfg, nil
}
