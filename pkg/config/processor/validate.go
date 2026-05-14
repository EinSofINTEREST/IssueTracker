package processorcfg

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"issuetracker/pkg/config/internal/parse"
)

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

	// ReparseEnabled: validate 실패 시 parser 재학습 트리거 활성화 여부 (이슈 #364).
	// VALIDATE_REPARSE_ENABLED (default: false — Sub C wiring 완료 후 활성화)
	// false 면 reparse 트리거 자체가 비활성 — 기존 DLQ 경로 그대로 동작 (회귀 안전).
	ReparseEnabled bool
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
		ReparseEnabled:            false, // Sub C 머지 후 활성화 (이슈 #366)
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

	// 검증 강화 (이슈 #439): 모든 길이/카운트는 양수, 모든 ratio/threshold/weight 는 [0.0, 1.0].
	for _, op := range []error{
		parse.Bool("VALIDATE_REPARSE_ENABLED", &cfg.ReparseEnabled),
		parse.PositiveInt("VALIDATE_NEWS_TITLE_MIN_LEN", &cfg.NewsTitleMinLen),
		parse.PositiveInt("VALIDATE_NEWS_TITLE_MAX_LEN", &cfg.NewsTitleMaxLen),
		parse.PositiveInt("VALIDATE_NEWS_BODY_MIN_LEN", &cfg.NewsBodyMinLen),
		parse.PositiveInt("VALIDATE_NEWS_BODY_MAX_LEN", &cfg.NewsBodyMaxLen),
		parse.PositiveInt("VALIDATE_NEWS_MIN_WORD_COUNT", &cfg.NewsMinWordCount),
		parse.Ratio("VALIDATE_NEWS_QUALITY_THRESHOLD", &cfg.NewsQualityThreshold),
		parse.Ratio("VALIDATE_NEWS_MAX_CAP_RATIO", &cfg.NewsMaxCapRatio),
		parse.Ratio("VALIDATE_NEWS_MAX_PUNCT_RATIO", &cfg.NewsMaxPunctRatio),
		parse.Ratio("VALIDATE_NEWS_WEIGHT_WORD_COUNT", &cfg.NewsWeightWordCount),
		parse.Ratio("VALIDATE_NEWS_WEIGHT_META", &cfg.NewsWeightMeta),
		parse.Ratio("VALIDATE_NEWS_WEIGHT_STRUCTURE", &cfg.NewsWeightStructure),
		parse.PositiveInt("VALIDATE_COMMUNITY_BODY_MIN_LEN", &cfg.CommunityBodyMinLen),
		parse.Ratio("VALIDATE_COMMUNITY_QUALITY_THRESHOLD", &cfg.CommunityQualityThreshold),
		parse.Ratio("VALIDATE_COMMUNITY_MAX_REPEAT_RATIO", &cfg.CommunityMaxRepeatRatio),
		parse.PositiveInt("VALIDATE_COMMUNITY_MIN_REPEAT_RUN", &cfg.CommunityMinRepeatRun),
	} {
		if op != nil {
			return ValidateConfig{}, op
		}
	}

	// 의미적 일관성 검사: Min < Max.
	if cfg.NewsTitleMinLen > cfg.NewsTitleMaxLen {
		return ValidateConfig{}, fmt.Errorf("VALIDATE_NEWS_TITLE_MIN_LEN (%d) cannot exceed _MAX_LEN (%d)", cfg.NewsTitleMinLen, cfg.NewsTitleMaxLen)
	}
	if cfg.NewsBodyMinLen > cfg.NewsBodyMaxLen {
		return ValidateConfig{}, fmt.Errorf("VALIDATE_NEWS_BODY_MIN_LEN (%d) cannot exceed _MAX_LEN (%d)", cfg.NewsBodyMinLen, cfg.NewsBodyMaxLen)
	}

	return cfg, nil
}
