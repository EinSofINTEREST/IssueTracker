// cost_guard.go 는 enrichment 호출 비용의 운영 안전장치입니다 (이슈 #456).
//
// enrich worker 는 validate 를 통과한 **모든** article 에 대해 claudegen 세션을 최대 4회
// (extract / verify / context / score) 호출합니다 (메타 #445 결정 4 — 선별 없음).
// claudegen 은 Claude Code 컨테이너에서 LLM + WebFetch 를 수행하므로 비용이 누적되고,
// validate backlog 가 폭증하면 enrich 가 따라가며 비용 spike 가 발생할 수 있습니다.
//
// 본 가드는 두 가지 조건으로 enrichment 를 **건너뜁니다**:
//
//  1. 일일 호출 한도 초과 (ENRICH_DAILY_CALL_LIMIT)
//  2. validate→enrich consumer lag 이 임계 초과 (ENRICH_MAX_BACKLOG)
//
// **forward-first 정책은 유지됩니다** — 건너뛰는 것은 enrichment 뿐이고, 메시지는 facts 없이
// 그대로 다음 단계로 발행됩니다. 가드가 파이프라인 진행을 막지 않습니다.
//
// 한계 — 일일 카운터는 **프로세스 로컬** 입니다. 멀티 인스턴스 운영 시 인스턴스 수만큼
// 한도가 배수로 늘어납니다 (이슈 #289 의 분산 상태 과제와 동일 성격). 현재는 단일 인스턴스
// 운영이 전제이며, 필요해지면 Redis 카운터로 옮깁니다.

package worker

import (
	"context"
	"sync"
	"time"

	"issuetracker/pkg/logger"
)

// SkipReason 은 enrichment 를 건너뛴 이유입니다 — metric label 로도 쓰입니다.
type SkipReason string

const (
	// SkipReasonNone: 건너뛰지 않음.
	SkipReasonNone SkipReason = ""
	// SkipReasonDailyBudget: 일일 호출 한도 초과.
	SkipReasonDailyBudget SkipReason = "daily_budget"
	// SkipReasonBacklog: consumer lag 임계 초과.
	SkipReasonBacklog SkipReason = "backlog"
)

// BacklogChecker 는 consumer lag 조회를 추상화합니다 — pkg/queue.BacklogChecker 와 동일 시그니처.
//
// 인터페이스를 여기서 다시 선언해 worker 패키지가 pkg/queue 구현에 직접 묶이지 않게 하고,
// 단위 테스트에서 stub 으로 교체할 수 있게 합니다.
type BacklogChecker interface {
	Backlog(ctx context.Context, topic string, group string) (int64, error)
}

// CostGuardConfig 는 CostGuard 의 동작을 제어합니다.
type CostGuardConfig struct {
	// DailyCallLimit 은 하루(UTC) 최대 enrichment 호출 수입니다. 0 이하면 무제한.
	DailyCallLimit int

	// MaxBacklog 는 enrichment 를 건너뛰기 시작하는 consumer lag 임계입니다. 0 이하면 비활성.
	MaxBacklog int64

	// BacklogTopic / BacklogGroup 은 lag 조회 대상입니다. MaxBacklog > 0 일 때만 사용.
	BacklogTopic string
	BacklogGroup string

	// BacklogCheckInterval 은 lag 재조회 주기입니다. 매 메시지마다 Kafka 에 물으면
	// 비용을 줄이려다 오히려 부하를 만들므로 캐시합니다. 0 이하면 기본 10s.
	BacklogCheckInterval time.Duration
}

// DefaultBacklogCheckInterval 은 lag 조회 결과의 캐시 수명입니다.
const DefaultBacklogCheckInterval = 10 * time.Second

// CostGuard 는 일일 한도와 backlog 임계를 확인합니다.
//
// goroutine-safe — worker pool 의 여러 goroutine 이 동시에 호출합니다.
type CostGuard struct {
	cfg     CostGuardConfig
	checker BacklogChecker
	log     *logger.Logger
	metrics *CostMetrics

	// now 는 테스트에서 날짜 경계를 제어하기 위한 seam 입니다. nil 이면 time.Now.
	now func() time.Time

	mu sync.Mutex
	// day 는 현재 카운터가 속한 UTC 날짜 (YYYYMMDD) 입니다. 바뀌면 count 를 0 으로 되돌립니다.
	day int
	// count 는 day 동안 수행한 enrichment 수입니다.
	count int

	// backlog 캐시 — checkedAt 이 interval 이내면 lastBacklog 를 재사용합니다.
	checkedAt   time.Time
	lastBacklog int64

	// blockedBy 는 현재 차단 중인 사유입니다 (SkipReasonNone 이면 통과 상태).
	// 메시지마다 로그를 남기면 한도 초과 후 입력 1건당 1줄이 쏟아지므로,
	// **상태가 바뀌는 순간에만** 로그합니다 (CodeRabbit 피드백).
	blockedBy SkipReason
}

// NewCostGuard 는 CostGuard 를 생성합니다.
//
// checker 가 nil 이면 backlog throttle 은 비활성 (DailyCallLimit 만 동작).
// metrics 가 nil 이어도 안전 — Record* 가 noop.
func NewCostGuard(cfg CostGuardConfig, checker BacklogChecker, metrics *CostMetrics, log *logger.Logger) *CostGuard {
	if cfg.BacklogCheckInterval <= 0 {
		cfg.BacklogCheckInterval = DefaultBacklogCheckInterval
	}
	return &CostGuard{cfg: cfg, checker: checker, metrics: metrics, log: log}
}

// Enabled 는 가드가 실제로 무언가를 검사하는지 반환합니다.
//
// 둘 다 비활성이면 호출자가 조회 자체를 건너뛸 수 있습니다.
func (g *CostGuard) Enabled() bool {
	if g == nil {
		return false
	}
	return g.cfg.DailyCallLimit > 0 || (g.cfg.MaxBacklog > 0 && g.checker != nil)
}

// Allow 는 enrichment 를 수행해도 되는지 판단하고, 허용 시 일일 카운터를 증가시킵니다.
//
// 반환 (true, SkipReasonNone) 이면 진행. (false, reason) 이면 enrichment 를 건너뛰고
// forward 만 해야 합니다.
//
// **카운터 증가와 판단이 한 임계구역 안에서 일어납니다** — 따로 두면 동시 호출이 한도를
// 넘겨 통과할 수 있습니다.
func (g *CostGuard) Allow(ctx context.Context) (bool, SkipReason) {
	if g == nil {
		return true, SkipReasonNone
	}

	// backlog 는 Kafka RPC 라 lock 밖에서 확인합니다 — lock 을 쥔 채 네트워크를 기다리면
	// 모든 worker goroutine 이 직렬화됩니다.
	if reason := g.checkBacklog(ctx); reason != SkipReasonNone {
		g.metrics.RecordDropped(string(reason))
		g.noteState(reason)
		return false, reason
	}

	// 판단은 lock 안에서 끝내고, 부수효과(metric / 로그)는 밖에서 합니다.
	// lock 을 쥔 채 로깅하면 worker 들이 직렬화되고, 중간에 풀었다 다시 잡으면
	// 교착과 상태 꼬임의 원인이 됩니다.
	allowed := g.consumeDailyBudget()

	if !allowed {
		g.metrics.RecordDropped(string(SkipReasonDailyBudget))
		g.noteState(SkipReasonDailyBudget)
		return false, SkipReasonDailyBudget
	}
	g.metrics.RecordCall()
	g.noteState(SkipReasonNone)
	return true, SkipReasonNone
}

// consumeDailyBudget 는 날짜 경계를 처리하고 한도 안이면 카운터를 증가시킵니다.
//
// **판단과 증가가 한 임계구역 안에서 일어납니다** — 나누면 동시 호출이 한도를 넘겨
// 통과합니다. 한도가 0 이하면 항상 true (무제한).
func (g *CostGuard) consumeDailyBudget() bool {
	if g.cfg.DailyCallLimit <= 0 {
		return true
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if today := g.currentDay(); today != g.day {
		g.day = today
		g.count = 0
	}
	if g.count >= g.cfg.DailyCallLimit {
		return false
	}
	g.count++
	return true
}

// checkBacklog 는 캐시된 lag 으로 임계 초과 여부를 판단합니다.
//
// 조회 실패는 **통과** 로 처리합니다 — Kafka 일시 장애 때문에 enrichment 를 멈추면
// 가드가 장애를 증폭시킵니다. 실패는 WARN 으로 남겨 운영자가 인지하게 합니다.
func (g *CostGuard) checkBacklog(ctx context.Context) SkipReason {
	if g.cfg.MaxBacklog <= 0 || g.checker == nil {
		return SkipReasonNone
	}

	g.mu.Lock()
	cached := g.lastBacklog
	fresh := !g.checkedAt.IsZero() && g.nowFn().Sub(g.checkedAt) < g.cfg.BacklogCheckInterval
	g.mu.Unlock()

	if fresh {
		if cached > g.cfg.MaxBacklog {
			return SkipReasonBacklog
		}
		return SkipReasonNone
	}

	lag, err := g.checker.Backlog(ctx, g.cfg.BacklogTopic, g.cfg.BacklogGroup)
	if err != nil {
		// **실패도 캐시한다** (CodeRabbit 피드백). checkedAt 을 갱신하지 않으면 Kafka 장애
		// 동안 모든 메시지가 새 RPC 를 띄우고 각자 타임아웃을 기다리며 WARN 을 남긴다 —
		// 장애를 견디려는 가드가 오히려 파이프라인을 느리게 만든다.
		// lag 0 으로 기록해 interval 동안 통과시킨다.
		g.mu.Lock()
		g.lastBacklog = 0
		g.checkedAt = g.nowFn()
		g.mu.Unlock()

		if g.log != nil && ctx.Err() == nil {
			g.log.WithFields(map[string]interface{}{
				"topic":          g.cfg.BacklogTopic,
				"retry_after_ms": g.cfg.BacklogCheckInterval.Milliseconds(),
			}).WithError(err).Warn("enrich backlog check failed, allowing enrichment until next check")
		}
		return SkipReasonNone
	}

	g.mu.Lock()
	g.lastBacklog = lag
	g.checkedAt = g.nowFn()
	g.mu.Unlock()

	if lag > g.cfg.MaxBacklog {
		return SkipReasonBacklog
	}
	return SkipReasonNone
}

// noteState 는 차단 상태가 바뀔 때만 로그를 남깁니다.
//
// 반환값은 "이번 호출이 전이였는지" — 호출자가 per-message 로그를 Debug 로 낮출지
// 판단하는 데 씁니다.
func (g *CostGuard) noteState(reason SkipReason) {
	g.mu.Lock()
	prev := g.blockedBy
	g.blockedBy = reason
	g.mu.Unlock()

	if prev == reason || g.log == nil {
		return
	}
	if reason == SkipReasonNone {
		g.log.WithField("previous_reason", string(prev)).
			Info("enrich cost guard released, enrichment resumed")
		return
	}
	g.log.WithFields(map[string]interface{}{
		"reason":           string(reason),
		"daily_call_limit": g.cfg.DailyCallLimit,
		"max_backlog":      g.cfg.MaxBacklog,
	}).Warn("enrich cost guard engaged, skipping enrichment until it clears")
}

func (g *CostGuard) nowFn() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// currentDay 는 UTC 기준 YYYYMMDD 정수입니다.
func (g *CostGuard) currentDay() int {
	t := g.nowFn().UTC()
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}

// ZSetLen 은 우선순위 ZSET 의 대기 항목 수 조회를 추상화합니다 (이슈 #456).
//
// *queue.PriorityZSetQueue 의 Len 이 그대로 만족합니다.
type ZSetLen interface {
	Len(ctx context.Context) (int64, error)
}

// zsetBacklogChecker 는 ZSET 길이를 backlog 로 보고하는 BacklogChecker 입니다.
//
// # 왜 필요한가
//
// ZSET 인입 모드에서는 intake 가 Kafka 메시지를 ZSET 으로 옮기고 **곧바로 commit** 합니다.
// 따라서 처리 대기 물량이 ZSET 에 쌓여 있어도 Kafka lag 은 0 에 가깝습니다. Kafka 기준
// 체커를 그대로 쓰면 ENRICH_MAX_BACKLOG 가 영영 발동하지 않습니다 (CodeRabbit 피드백).
//
// topic / group 인자는 무시합니다 — ZSET 은 단일 키라 구분이 필요 없습니다.
type zsetBacklogChecker struct {
	q ZSetLen
}

// NewZSetBacklogChecker 는 ZSET 길이를 backlog 로 보고하는 checker 를 만듭니다.
// q 가 nil 이면 nil 을 반환 — 호출자가 Kafka checker 로 fallback 하면 됩니다.
func NewZSetBacklogChecker(q ZSetLen) BacklogChecker {
	if q == nil {
		return nil
	}
	return &zsetBacklogChecker{q: q}
}

func (c *zsetBacklogChecker) Backlog(ctx context.Context, _ string, _ string) (int64, error) {
	return c.q.Len(ctx)
}
