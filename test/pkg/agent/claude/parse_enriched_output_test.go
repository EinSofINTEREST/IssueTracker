package claude_test

// parseEnrichedOutput 은 claudegen 패키지의 unexported 헬퍼라 동일 구조의 동작은 통합 테스트
// (worker_test.go 의 ExtractEnriched flow) 로 검증됩니다. 본 파일은 ExtractResult 의 schema
// 호환성 — JSON unmarshal contract — 를 별도로 고정하여 향후 schema drift 회귀를 방지합니다.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage/model"
)

// TestExtractResult_BlacklistDecision_JSONShape 은 BlacklistDecision 이 운영자 manual review
// 시 사용 가능한 형태로 JSON 직렬화되는지 검증합니다 (운영 dashboard / DB INSERT 전 dry-run).
func TestExtractResult_BlacklistDecision_JSONShape(t *testing.T) {
	res := llmgen.ExtractResult{
		Blacklist: &llmgen.BlacklistDecision{Reason: "광고 페이지"},
	}

	data, err := json.Marshal(res)
	require.NoError(t, err)

	// JSON 키가 의도된 camelCase / snake_case 정책에 따라 직렬화되는지는 application
	// 측 검증 — 현재 ExtractResult 는 Go default tag 없는 PascalCase exported 필드.
	var unmarshalled struct {
		Blacklist *struct {
			Reason string `json:"Reason"`
			Mode   string `json:"Mode"`
		} `json:"Blacklist"`
	}
	require.NoError(t, json.Unmarshal(data, &unmarshalled))
	require.NotNil(t, unmarshalled.Blacklist)
	assert.Equal(t, "광고 페이지", unmarshalled.Blacklist.Reason)
	assert.Equal(t, "", unmarshalled.Blacklist.Mode, "Mode 미설정 시 빈 문자열 직렬화 — service 가 default drop 으로 fallback")
}

// TestExtractResult_BlacklistDecision_ModeField 는 Mode 필드가 두 mode 값에 대해 round-trip
// 직렬화/역직렬화 되는지 검증합니다 (이슈 #480).
func TestExtractResult_BlacklistDecision_ModeField(t *testing.T) {
	cases := []model.BlacklistMode{model.BlacklistModeDrop, model.BlacklistModeExtractLinksOnly}
	for _, want := range cases {
		t.Run(string(want), func(t *testing.T) {
			res := llmgen.ExtractResult{
				Blacklist: &llmgen.BlacklistDecision{
					Reason: "카테고리 인덱스",
					Mode:   want,
				},
			}
			data, err := json.Marshal(res)
			require.NoError(t, err)

			var roundtrip llmgen.ExtractResult
			require.NoError(t, json.Unmarshal(data, &roundtrip))
			require.NotNil(t, roundtrip.Blacklist)
			assert.Equal(t, want, roundtrip.Blacklist.Mode)
		})
	}
}

// TestPageType_Constants 는 claudegen prompt 와 storage layer 사이의 분류 string 계약이
// 깨지지 않도록 상수 값을 고정합니다.
func TestPageType_Constants(t *testing.T) {
	cases := map[llmgen.PageType]string{
		llmgen.PageTypeNews:        "news",
		llmgen.PageTypeCommunity:   "community",
		llmgen.PageTypeInfo:        "info",
		llmgen.PageTypeCommercial:  "commercial",
		llmgen.PageTypePaper:       "paper",
		llmgen.PageTypeOther:       "other",
		llmgen.PageTypeUnspecified: "",
	}
	for pt, want := range cases {
		assert.Equal(t, want, string(pt))
	}
}
