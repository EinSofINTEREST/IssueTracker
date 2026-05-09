package llmgen_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage"
)

func TestHostBreaker_AllowsWhenNoFailures(t *testing.T) {
	t.Parallel()
	b := llmgen.NewHostBreaker(llmgen.HostBreakerConfig{FailureThreshold: 3, CooldownDuration: time.Minute})
	allowed, remaining := b.Allow("example.com", storage.TargetTypePage)
	assert.True(t, allowed)
	assert.Zero(t, remaining)
}

func TestHostBreaker_BlocksAfterThresholdConsecutiveRateLimits(t *testing.T) {
	t.Parallel()
	b := llmgen.NewHostBreaker(llmgen.HostBreakerConfig{FailureThreshold: 3, CooldownDuration: time.Minute})

	for i := 0; i < 2; i++ {
		b.RecordRateLimit("example.com", storage.TargetTypePage)
		allowed, _ := b.Allow("example.com", storage.TargetTypePage)
		assert.True(t, allowed, "threshold 도달 전에는 통과")
	}

	b.RecordRateLimit("example.com", storage.TargetTypePage)
	allowed, remaining := b.Allow("example.com", storage.TargetTypePage)
	assert.False(t, allowed, "3회 도달 시 cooldown 진입")
	assert.Greater(t, remaining, time.Duration(0))
}

func TestHostBreaker_SuccessResetsConsecutiveCount(t *testing.T) {
	t.Parallel()
	b := llmgen.NewHostBreaker(llmgen.HostBreakerConfig{FailureThreshold: 3, CooldownDuration: time.Minute})

	b.RecordRateLimit("example.com", storage.TargetTypePage)
	b.RecordRateLimit("example.com", storage.TargetTypePage)
	b.RecordSuccess("example.com", storage.TargetTypePage)

	// 다시 2회 — 성공 reset 후이므로 아직 차단 X.
	b.RecordRateLimit("example.com", storage.TargetTypePage)
	b.RecordRateLimit("example.com", storage.TargetTypePage)
	allowed, _ := b.Allow("example.com", storage.TargetTypePage)
	assert.True(t, allowed, "성공으로 카운터 reset 됨")
}

func TestHostBreaker_CooldownExpiresAndAllows(t *testing.T) {
	t.Parallel()
	b := llmgen.NewHostBreaker(llmgen.HostBreakerConfig{FailureThreshold: 2, CooldownDuration: 10 * time.Millisecond})

	b.RecordRateLimit("example.com", storage.TargetTypePage)
	b.RecordRateLimit("example.com", storage.TargetTypePage)

	allowed, _ := b.Allow("example.com", storage.TargetTypePage)
	assert.False(t, allowed, "차단 진입")

	time.Sleep(15 * time.Millisecond)

	allowed, _ = b.Allow("example.com", storage.TargetTypePage)
	assert.True(t, allowed, "cooldown 만료 후 다시 통과")
}

func TestHostBreaker_PerTargetTypeIsolated(t *testing.T) {
	t.Parallel()
	b := llmgen.NewHostBreaker(llmgen.HostBreakerConfig{FailureThreshold: 2, CooldownDuration: time.Minute})

	// page 만 차단.
	b.RecordRateLimit("example.com", storage.TargetTypePage)
	b.RecordRateLimit("example.com", storage.TargetTypePage)

	pageAllowed, _ := b.Allow("example.com", storage.TargetTypePage)
	listAllowed, _ := b.Allow("example.com", storage.TargetTypeList)
	assert.False(t, pageAllowed, "page 차단")
	assert.True(t, listAllowed, "list 는 영향 없음")
}

func TestHostBreaker_PerHostIsolated(t *testing.T) {
	t.Parallel()
	b := llmgen.NewHostBreaker(llmgen.HostBreakerConfig{FailureThreshold: 2, CooldownDuration: time.Minute})

	b.RecordRateLimit("a.com", storage.TargetTypePage)
	b.RecordRateLimit("a.com", storage.TargetTypePage)

	aAllowed, _ := b.Allow("a.com", storage.TargetTypePage)
	bAllowed, _ := b.Allow("b.com", storage.TargetTypePage)
	assert.False(t, aAllowed, "a.com 차단")
	assert.True(t, bAllowed, "b.com 영향 없음")
}

func TestHostBreaker_DefaultConfigAppliedOnZero(t *testing.T) {
	t.Parallel()
	// MaxAttempts=0, Cooldown=0 → DefaultHostBreakerConfig() 적용.
	b := llmgen.NewHostBreaker(llmgen.HostBreakerConfig{})
	def := llmgen.DefaultHostBreakerConfig()

	for i := 0; i < def.FailureThreshold-1; i++ {
		b.RecordRateLimit("h.com", storage.TargetTypePage)
		allowed, _ := b.Allow("h.com", storage.TargetTypePage)
		assert.True(t, allowed)
	}
	b.RecordRateLimit("h.com", storage.TargetTypePage)
	allowed, _ := b.Allow("h.com", storage.TargetTypePage)
	assert.False(t, allowed, "default threshold 도달 시 차단")
}

func TestHostBreaker_SnapshotReportsBlockedHosts(t *testing.T) {
	t.Parallel()
	b := llmgen.NewHostBreaker(llmgen.HostBreakerConfig{FailureThreshold: 2, CooldownDuration: time.Minute})

	b.RecordRateLimit("a.com", storage.TargetTypePage)
	b.RecordRateLimit("a.com", storage.TargetTypePage)
	b.RecordRateLimit("b.com", storage.TargetTypeList)

	snap := b.Snapshot()
	assert.Len(t, snap, 2)

	// a.com 은 BlockedUntil 미래 — b.com 은 1회만이므로 zero.
	for _, s := range snap {
		switch s.Host {
		case "a.com":
			assert.False(t, s.BlockedUntil.IsZero())
			assert.GreaterOrEqual(t, s.Failures, 2)
		case "b.com":
			assert.True(t, s.BlockedUntil.IsZero())
			assert.Equal(t, 1, s.Failures)
		}
	}
}
