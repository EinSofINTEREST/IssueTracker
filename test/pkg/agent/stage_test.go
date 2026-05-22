// StageEnv 의 prefix 정규화 + Get/GetOr 우선순위 검증 (이슈 #530 — agent 공용 재설계).
//
// 본 테스트는 agent 패키지의 generic 동작만 검증 — 각 provider (claude/codex 등) 의
// wiring 동작은 별도 테스트 파일에서 검증한다.
package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/agent"
)

func TestStageEnv_Prefix(t *testing.T) {
	cases := []struct {
		name  string
		stage string
		want  string
	}{
		{"empty", "", ""},
		{"simple", "parser", "PARSER_"},
		{"hyphen", "parser-llm", "PARSER_LLM_"},
		{"space", "validate gate", "VALIDATE_GATE_"},
		{"dot", "stage.x", "STAGE_X_"},
		{"mixed", "Parser-Stage 1", "PARSER_STAGE_1_"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := agent.NewStageEnv(tt.stage).Prefix()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStageEnv_Get_StagePrefixTakesPrecedence(t *testing.T) {
	t.Setenv("BASE_VAR", "base-value")
	t.Setenv("PARSER_BASE_VAR", "stage-value")

	val, key := agent.NewStageEnv("parser").Get("BASE_VAR")
	assert.Equal(t, "stage-value", val)
	assert.Equal(t, "PARSER_BASE_VAR", key)
}

func TestStageEnv_Get_FallsBackToBase(t *testing.T) {
	t.Setenv("BASE_VAR", "base-value")
	t.Setenv("PARSER_BASE_VAR", "") // 명시적 미설정

	val, key := agent.NewStageEnv("parser").Get("BASE_VAR")
	assert.Equal(t, "base-value", val)
	assert.Equal(t, "BASE_VAR", key)
}

func TestStageEnv_Get_BothMissing_ReturnsEmpty(t *testing.T) {
	t.Setenv("BASE_VAR", "")
	t.Setenv("PARSER_BASE_VAR", "")

	val, key := agent.NewStageEnv("parser").Get("BASE_VAR")
	assert.Equal(t, "", val)
	assert.Equal(t, "BASE_VAR", key, "key 는 base 그대로 반환 — 미설정 진단용")
}

func TestStageEnv_Get_EmptyName_BypassesPrefix(t *testing.T) {
	t.Setenv("BASE_VAR", "base-value")
	t.Setenv("PARSER_BASE_VAR", "stage-value") // Name="" 이라 무시되어야 함

	val, key := agent.NewStageEnv("").Get("BASE_VAR")
	assert.Equal(t, "base-value", val)
	assert.Equal(t, "BASE_VAR", key)
}

func TestStageEnv_GetOr_Default(t *testing.T) {
	t.Setenv("BASE_VAR", "")
	t.Setenv("PARSER_BASE_VAR", "")

	got := agent.NewStageEnv("parser").GetOr("BASE_VAR", "default-value")
	assert.Equal(t, "default-value", got)
}

func TestStageEnv_GetOr_StageValue(t *testing.T) {
	t.Setenv("PARSER_BASE_VAR", "stage-value")
	got := agent.NewStageEnv("parser").GetOr("BASE_VAR", "default-value")
	assert.Equal(t, "stage-value", got)
}

func TestStageEnv_Name(t *testing.T) {
	assert.Equal(t, "parser", agent.NewStageEnv("parser").Name())
	assert.Equal(t, "", agent.NewStageEnv("").Name())
}

// TestPoolConfig_Embedding: agent.PoolConfig 가 다른 provider 의 config 에 embed 가능.
// (컴파일 검증 — runtime assert 없음)
func TestPoolConfig_Embedding(t *testing.T) {
	type customConfig struct {
		agent.PoolConfig
		ExtraField string
	}
	cfg := customConfig{
		PoolConfig: agent.PoolConfig{Name: "parser"},
		ExtraField: "custom",
	}
	assert.Equal(t, "parser", cfg.Name)
	assert.Equal(t, "custom", cfg.ExtraField)
}
