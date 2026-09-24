package scoring

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// Aggregator 는 host 별 signal 집계 원천입니다.
//
// 인터페이스로 둔 이유: scorer 는 집계 방법(현재는 contents 질의)에 의존하지 않아야
// cost/reliability signal 이 추가될 때 scorer 를 고치지 않는다.
type Aggregator interface {
	Aggregate(ctx context.Context, windowMinutes int) ([]HostAggregate, error)
}

// HostAggregate 는 집계 결과를 scorer 가 다루는 형태로 옮긴 것입니다.
type HostAggregate struct {
	Host    string
	Signals model.HostSignals
}

// ScoreSink 는 계산된 점수 스냅샷을 받는 쪽입니다 (resolver).
type ScoreSink interface {
	SetScores(scores map[string]float64)
}

// defaultAggregateTimeout 은 AggregateTimeout 미설정 시 적용할 기본 상한입니다.
const defaultAggregateTimeout = 30 * time.Second

// Config 는 scorer 동작 설정입니다.
type Config struct {
	// Interval 은 집계 주기입니다.
	Interval time.Duration
	// WindowMinutes 는 집계 구간입니다.
	WindowMinutes int
	// Weights 는 signal 가중치입니다.
	Weights Weights
	// AggregateTimeout 은 집계 질의 1회의 상한입니다.
	//
	// Interval 보다 짧아야 한다 — 길면 다음 주기가 도래해도 이전 질의가 아직 돌고 있다.
	AggregateTimeout time.Duration
}

// Scorer 는 주기적으로 signal 을 집계해 점수를 저장하고 resolver 에 스냅샷을 공급합니다
// (이슈 #382).
//
// 실패 정책: 한 주기의 집계 / 저장 실패는 **이전 스냅샷을 유지** 한 채 다음 주기를 기다린다.
// 실패 시 스냅샷을 비우면 DB 일시 장애가 곧바로 전체 host 의 우선순위 변동으로 번진다.
type Scorer struct {
	agg  Aggregator
	repo repository.HostScoringRepository
	sink ScoreSink
	cfg  Config
	log  *logger.Logger

	stopOnce sync.Once
	stopped  chan struct{}
	wg       sync.WaitGroup
}

// NewScorer 는 Scorer 를 생성합니다.
func NewScorer(
	agg Aggregator,
	repo repository.HostScoringRepository,
	sink ScoreSink,
	cfg Config,
	log *logger.Logger,
) *Scorer {
	if cfg.AggregateTimeout <= 0 {
		// 0 을 그대로 두면 context.WithTimeout 이 즉시 만료돼 **매 주기가 실패** 한다.
		// 호출자가 설정을 빠뜨렸을 때 기능이 조용히 죽는 것을 막는다.
		cfg.AggregateTimeout = defaultAggregateTimeout
	}
	return &Scorer{
		agg:     agg,
		repo:    repo,
		sink:    sink,
		cfg:     cfg,
		log:     log,
		stopped: make(chan struct{}),
	}
}

// Start 는 주기 goroutine 을 시작합니다.
//
// 부팅 직후 1회를 먼저 수행한다 — 첫 주기를 기다리면 그동안 resolver 가 비어 있어
// 재시작할 때마다 Interval 만큼 기능이 꺼진 상태가 된다.
func (s *Scorer) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		s.runOnce(ctx)

		ticker := time.NewTicker(s.cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stopped:
				return
			case <-ticker.C:
				s.runOnce(ctx)
			}
		}
	}()
}

// Stop 은 goroutine 종료를 요청하고 완료를 대기합니다. 멱등.
func (s *Scorer) Stop() {
	s.stopOnce.Do(func() { close(s.stopped) })
	s.wg.Wait()
}

// runOnce 는 한 주기를 수행합니다 — 집계 → 점수 계산 → 저장 → 스냅샷 공급.
func (s *Scorer) runOnce(ctx context.Context) {
	// 집계 질의에 상한을 건다 — root ctx 로 도는 주기 작업이라 상한이 없으면 느린 질의
	// 하나가 주기를 통째로 묶고, 그동안 스냅샷이 갱신되지 않는다.
	aggCtx, cancel := context.WithTimeout(ctx, s.cfg.AggregateTimeout)
	defer cancel()

	aggregates, err := s.agg.Aggregate(aggCtx, s.cfg.WindowMinutes)
	if err != nil {
		// 이전 스냅샷을 유지한다 (SetScores 미호출).
		s.log.WithError(err).Warn("host scoring aggregate failed, keeping previous snapshot")
		return
	}

	scores := make(map[string]float64, len(aggregates))
	var coldStart int

	for _, a := range aggregates {
		score, ok := Score(a.Signals, s.cfg.Weights)
		if !ok {
			// 표본 부족 — 점수를 내지 않고 chain 의 기존 경로에 위임한다.
			coldStart++
			continue
		}
		scores[a.Host] = score

		raw, merr := json.Marshal(a.Signals)
		if merr != nil {
			// 지문 직렬화 실패가 점수 저장을 막지 않도록 빈 객체로 저장한다.
			s.log.WithField("host", a.Host).WithError(merr).
				Warn("host signals marshal failed, storing empty snapshot")
			raw = []byte(`{}`)
		}

		if uerr := s.repo.Upsert(ctx, &model.HostScoringState{
			Host:          a.Host,
			Score:         score,
			Signals:       raw,
			WindowMinutes: s.cfg.WindowMinutes,
		}); uerr != nil {
			// 저장 실패는 이번 주기의 그 host 만 영향 — 스냅샷에는 남겨 둔다.
			// 점수 자체는 메모리에서 유효하고, 다음 주기에 다시 저장을 시도한다.
			s.log.WithField("host", a.Host).WithError(uerr).
				Warn("host scoring upsert failed")
		}
	}

	s.sink.SetScores(scores)

	s.log.WithFields(map[string]interface{}{
		"scored_hosts":   len(scores),
		"cold_start":     coldStart,
		"window_minutes": s.cfg.WindowMinutes,
	}).Info("host scoring cycle completed")
}
