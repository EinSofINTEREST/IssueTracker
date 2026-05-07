package worker_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/parser/worker"
)

// TestShouldEnqueueLLMOnNoRule 는 HasAnyRule 결과별 LLM Enqueue 결정을 검증합니다 (이슈 #287).
func TestShouldEnqueueLLMOnNoRule(t *testing.T) {
	tests := []struct {
		name       string
		exists     bool
		hasEnabled bool
		err        error
		want       bool
	}{
		{
			name:       "lookup error — fail-open enqueue (DB 일시 장애가 LLM 학습을 영구 차단하지 않도록)",
			exists:     false,
			hasEnabled: false,
			err:        errors.New("db down"),
			want:       true,
		},
		{
			name:       "lookup error with positive cached state — 여전히 fail-open enqueue",
			exists:     true,
			hasEnabled: false,
			err:        errors.New("redis timeout"),
			want:       true,
		},
		{
			name:       "no rule — enqueue (진짜 룰 부재)",
			exists:     false,
			hasEnabled: false,
			err:        nil,
			want:       true,
		},
		{
			name:       "enabled rule exists — enqueue (cache stale, generator pre-check 차단 예정)",
			exists:     true,
			hasEnabled: true,
			err:        nil,
			want:       true,
		},
		{
			name:       "disabled rule only — skip (운영자 의도된 disable 존중)",
			exists:     true,
			hasEnabled: false,
			err:        nil,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := worker.ShouldEnqueueLLMOnNoRule(tt.exists, tt.hasEnabled, tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}
