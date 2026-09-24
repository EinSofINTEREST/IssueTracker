// budget.go 는 LLM 호출 예산 가드입니다 (이슈 #169).
//
// rule generator 는 룰이 없는 host 를 만날 때마다 LLM 을 호출합니다. 신규 사이트가 몰리거나
// 룰 생성이 반복 실패하면 호출이 급증해 무료 한도(예: Gemini 1000/day)를 넘길 수 있습니다.
// 본 가드가 일/시간 두 창으로 상한을 강제합니다.
//
// # 분산 / 단독 동작
//
// Redis counter 를 주입하면 인스턴스들이 한도를 **공유** 합니다. 주입하지 않으면 프로세스
// 로컬 sliding window 로 degrade 하며, 이때는 인스턴스 수만큼 한도가 배수로 늘어납니다.
// Redis 부재로 LLM 호출 자체를 막지는 않습니다 — 예산 가드가 장애를 증폭시키면 안 됩니다.
//
// # 왜 sliding window 인가
//
// 고정 창(예: 자정 리셋)은 경계 직전/직후에 두 배가 몰립니다. sliding 은 임의 시점에서
// 직전 24h / 1h 를 재므로 그 스파이크가 없습니다.

package llmgen

import (
	"context"
	"sync"
	"time"

	"issuetracker/pkg/logger"
)

// 예산 창 이름 — Redis key suffix 이자 metric label 입니다.
const (
	BudgetWindowDaily  = "daily"
	BudgetWindowHourly = "hourly"
)

// BudgetCounter 는 sliding window 호출 카운터입니다.
//
// internal/storage/redis 의 구현체가 만족합니다. 인터페이스를 여기 두어 llmgen 이
// storage 구현에 직접 묶이지 않게 하고, 테스트에서 stub 으로 교체할 수 있게 합니다.
type BudgetCounter interface {
	// Record 는 호출 1건을 기록하고 window 안의 총 수를 반환합니다.
	Record(ctx context.Context, window string, span time.Duration) (int, error)
	// Count 는 기록 없이 window 안의 현재 수만 반환합니다.
	Count(ctx context.Context, window string, span time.Duration) (int, error)
}

// BudgetConfig 는 호출 상한입니다. 각 항목이 0 이하면 해당 창은 비활성입니다.
type BudgetConfig struct {
	DailyCap  int // LLM_DAILY_CAP  (default 1000 — Gemini 무료 한도)
	HourlyCap int // LLM_HOURLY_CAP (default 100)
}

// Enabled 는 둘 중 하나라도 상한이 걸려 있는지 반환합니다.
func (c BudgetConfig) Enabled() bool { return c.DailyCap > 0 || c.HourlyCap > 0 }

// CallBudget 는 LLM 호출 전 상한을 확인하고 통과 시 카운터를 올립니다.
//
// goroutine-safe.
type CallBudget struct {
	cfg     BudgetConfig
	counter BudgetCounter
	log     *logger.Logger
	metrics *GeneratorMetrics

	// local 은 Redis 부재 시 쓰는 프로세스 로컬 카운터입니다.
	mu    sync.Mutex
	local map[string][]time.Time

	// exceeded 는 현재 막혀 있는 창입니다 — 상태 전이 시에만 로그하기 위함.
	exceeded map[string]bool

	now func() time.Time
}

// NewCallBudget 는 CallBudget 를 생성합니다.
//
// counter 가 nil 이면 프로세스 로컬 카운터로 degrade 합니다.
// cfg 가 비활성이면 nil 을 반환 — 호출자가 가드 자체를 건너뛸 수 있습니다.
func NewCallBudget(cfg BudgetConfig, counter BudgetCounter, metrics *GeneratorMetrics, log *logger.Logger) *CallBudget {
	if !cfg.Enabled() {
		return nil
	}
	return &CallBudget{
		cfg:      cfg,
		counter:  counter,
		log:      log,
		metrics:  metrics,
		local:    map[string][]time.Time{},
		exceeded: map[string]bool{},
	}
}

func (b *CallBudget) nowFn() time.Time {
	if b != nil && b.now != nil {
		return b.now()
	}
	return time.Now()
}

// Allow 는 호출 가능 여부를 반환합니다. 가능하면 카운터를 올립니다.
//
// 반환 (false, window) 이면 그 창의 상한에 도달한 것입니다.
// 조회 실패는 **통과** 로 처리합니다 — Redis 일시 장애로 룰 생성을 멈추면 가드가 장애를
// 증폭시킵니다.
func (b *CallBudget) Allow(ctx context.Context) (bool, string) {
	if b == nil {
		return true, ""
	}

	// 상한이 작은 창부터 검사 — 시간당 한도가 먼저 걸리는 것이 일반적입니다.
	for _, w := range []struct {
		name string
		cap  int
		span time.Duration
	}{
		{BudgetWindowHourly, b.cfg.HourlyCap, time.Hour},
		{BudgetWindowDaily, b.cfg.DailyCap, 24 * time.Hour},
	} {
		if w.cap <= 0 {
			continue
		}
		count, err := b.count(ctx, w.name, w.span)
		if err != nil {
			if b.log != nil && ctx.Err() == nil {
				b.log.WithFields(map[string]interface{}{"window": w.name}).
					WithError(err).Warn("llm call budget check failed, allowing call")
			}
			continue
		}
		b.metrics.SetCapRemaining(w.name, float64(w.cap-count))
		if count >= w.cap {
			b.noteState(w.name, true, count, w.cap)
			return false, w.name
		}
		b.noteState(w.name, false, count, w.cap)
	}

	// 통과 — 두 창 모두에 기록한다.
	b.record(ctx)
	return true, ""
}

// count 는 window 의 현재 호출 수를 반환합니다.
func (b *CallBudget) count(ctx context.Context, window string, span time.Duration) (int, error) {
	if b.counter != nil {
		return b.counter.Count(ctx, window, span)
	}
	return b.localCount(window, span), nil
}

// record 는 활성화된 모든 창에 호출 1건을 기록합니다.
func (b *CallBudget) record(ctx context.Context) {
	for _, w := range []struct {
		name string
		cap  int
		span time.Duration
	}{
		{BudgetWindowHourly, b.cfg.HourlyCap, time.Hour},
		{BudgetWindowDaily, b.cfg.DailyCap, 24 * time.Hour},
	} {
		if w.cap <= 0 {
			continue
		}
		if b.counter != nil {
			if _, err := b.counter.Record(ctx, w.name, w.span); err != nil && b.log != nil && ctx.Err() == nil {
				b.log.WithFields(map[string]interface{}{"window": w.name}).
					WithError(err).Warn("llm call budget record failed")
			}
			continue
		}
		b.localRecord(w.name, w.span)
	}
}

// localCount 는 프로세스 로컬 sliding window 의 현재 수입니다.
func (b *CallBudget) localCount(window string, span time.Duration) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pruneLocked(window, span))
}

func (b *CallBudget) localRecord(window string, span time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.pruneLocked(window, span)
	b.local[window] = append(kept, b.nowFn())
}

// pruneLocked 는 window 밖의 기록을 제거하고 남은 슬라이스를 돌려줍니다. 호출자가 lock 보유.
func (b *CallBudget) pruneLocked(window string, span time.Duration) []time.Time {
	cutoff := b.nowFn().Add(-span)
	src := b.local[window]
	kept := src[:0]
	for _, t := range src {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.local[window] = kept
	return kept
}

// noteState 는 창이 막히거나 풀릴 때만 로그를 남깁니다.
//
// 호출마다 남기면 상한 도달 후 입력 1건당 1줄이 쏟아집니다 — 건수는 metric 이 셉니다.
func (b *CallBudget) noteState(window string, blocked bool, count, cap int) {
	b.mu.Lock()
	prev := b.exceeded[window]
	b.exceeded[window] = blocked
	b.mu.Unlock()

	if prev == blocked || b.log == nil {
		return
	}
	fields := map[string]interface{}{
		"window": window,
		"count":  count,
		"cap":    cap,
	}
	if blocked {
		b.log.WithFields(fields).Warn("llm call budget exceeded, skipping rule generation until window slides")
		return
	}
	b.log.WithFields(fields).Info("llm call budget recovered, rule generation resumed")
}
