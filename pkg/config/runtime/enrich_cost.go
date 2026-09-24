package runtimecfg

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"issuetracker/pkg/config/internal/parse"
)

// EnrichCostConfig 는 enrichment 호출 비용의 운영 안전장치 설정입니다 (이슈 #456).
//
// enrich worker 는 validate 를 통과한 **모든** article 에 claudegen 세션을 최대 4회
// (extract / verify / context / score) 호출합니다 (메타 #445 결정 4 — 선별 없음).
// backlog 가 폭증하면 enrich 가 따라가며 비용 spike 가 발생할 수 있어, 운영자가
// 상한을 걸 수 있게 합니다.
//
// 모든 기본값이 "기존 동작 유지" 입니다 — 한도 0(무제한), 토글 전부 true.
type EnrichCostConfig struct {
	// DailyCallLimit: 하루(UTC) 최대 enrichment 수행 수. 0 이하면 무제한.
	// 환경변수 ENRICH_DAILY_CALL_LIMIT (default 0).
	//
	// 카운터는 프로세스 로컬입니다 — 멀티 인스턴스에서는 인스턴스 수만큼 배수로 늘어납니다
	// (이슈 #289 의 분산 상태 과제와 동일 성격).
	DailyCallLimit int

	// MaxBacklog: validate→enrich consumer lag 임계. 초과 시 enrichment 를 건너뜁니다.
	// 0 이하면 비활성. 환경변수 ENRICH_MAX_BACKLOG (default 0).
	// scheduler 의 SCHEDULER_MAX_BACKLOG 와 동일 패턴.
	MaxBacklog int64

	// 단계별 토글 — 비용 spike 시 특정 단계만 즉시 끌 수 있게 합니다.
	// 전체 stage 토글 (STAGES_ENRICH_ENABLED) 보다 세밀한 제어.
	// 비활성 단계는 wiring 에서 Noop 구현체로 교체됩니다.
	ExtractEnabled bool // ENRICH_EXTRACT_ENABLED (default true)
	VerifyEnabled  bool // ENRICH_VERIFY_ENABLED  (default true)
	ContextEnabled bool // ENRICH_CONTEXT_ENABLED (default true)
	ScoreEnabled   bool // ENRICH_SCORE_ENABLED   (default true)
}

// DefaultEnrichCostConfig 는 기존 동작과 동일한 설정을 반환합니다 — 한도 없음, 전 단계 활성.
func DefaultEnrichCostConfig() EnrichCostConfig {
	return EnrichCostConfig{
		DailyCallLimit: 0,
		MaxBacklog:     0,
		ExtractEnabled: true,
		VerifyEnabled:  true,
		ContextEnabled: true,
		ScoreEnabled:   true,
	}
}

// LoadEnrichCost 는 .env 를 로드한 후 환경변수로 EnrichCostConfig 를 구성합니다.
func LoadEnrichCost(envFiles ...string) (EnrichCostConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return EnrichCostConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultEnrichCostConfig()

	for _, op := range []error{
		parse.NonNegativeInt("ENRICH_DAILY_CALL_LIMIT", &cfg.DailyCallLimit),
		parse.NonNegativeInt64("ENRICH_MAX_BACKLOG", &cfg.MaxBacklog),
		parse.Bool("ENRICH_EXTRACT_ENABLED", &cfg.ExtractEnabled),
		parse.Bool("ENRICH_VERIFY_ENABLED", &cfg.VerifyEnabled),
		parse.Bool("ENRICH_CONTEXT_ENABLED", &cfg.ContextEnabled),
		parse.Bool("ENRICH_SCORE_ENABLED", &cfg.ScoreEnabled),
	} {
		if op != nil {
			return EnrichCostConfig{}, op
		}
	}

	// 음수 거부는 parse.NonNegative* 가 담당한다 — 0 (비활성) 과 오타를 구분해 에러로 올린다.

	return cfg, nil
}
