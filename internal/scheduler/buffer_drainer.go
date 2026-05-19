package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// LeaderLocker 는 다중 인스턴스 환경에서 drainer leader election 을 추상화합니다 (이슈 #512).
//
// 구현체: pkg/redis.LeaderLocker (token-based SET NX EX + Lua release).
// nil 주입 시 BufferDrainer 는 단일 인스턴스 모드로 동작 (election 우회).
type LeaderLocker interface {
	// TryAcquire 는 leader 획득을 시도. 신규 leader 면 (true, nil), 다른 leader 존재 시 (false, nil),
	// Redis 인프라 에러 시 (false, err).
	TryAcquire(ctx context.Context) (bool, error)
	// Release 는 보유한 leader lock 을 ownership 확인 후 해제. 미보유 시 noop.
	Release(ctx context.Context) error
}

// BufferDrainer 는 Redis JobBuffer 에 적재된 normal/low priority crawl 메시지를
// Kafka backlog 가 임계 미만일 때 점진적으로 underlying Producer 로 publish 합니다 (이슈 #510).
//
// 다중 인스턴스 안전성 (이슈 #512):
//   - leader LeaderLocker (옵션) 가 wiring 되면 매 tick 시작 시 TryAcquire 로 leader 만 drain
//   - non-leader 인스턴스는 drain skip — 정상 양보 (ProcessingLock 의 "commit 없이 양보" 패턴과 동일)
//   - Election 인프라 에러 시 fail-closed — drain skip + warn (Redis 장애에서 모든 인스턴스 skip)
//
// 동작 (주기적 tick):
//  1. (옵션) leader 획득 시도. 비-leader 면 skip.
//  2. priority 별로 Kafka topic 의 현재 backlog (consumer-group lag) 를 BacklogChecker 로 조회
//  3. available = targetBacklog - currentBacklog
//  4. n := min(available, drainBatch, JobBuffer.JobBufferLen)
//  5. JobBuffer.DrainJobs(label, n) → underlying.PublishBatch
//  6. publish 실패 시 RetryScheduler 로 Kafka 재시도 경로 진입 (이슈 #512) — Redis 재적재 회피
//
// publish 실패 시 fetch 재시도 인프라 (bus.RetryScheduler) 와 통합 — Kafka 토픽에서 빠른 재시도 가능.
// Redis 재적재 (이전 동작) 는 다음 drain tick (default 30s) 까지 대기해야 했으나, RetryScheduler 는
// exponential backoff 후 즉시 Kafka 재publish → fetcher worker 가 즉시 pickup.
//
// Stop 호출 시 다음 tick 전에 종료. 이미 drain 중인 사이클은 완료까지 대기 (graceful).
type BufferDrainer struct {
	buffer         queue.JobBuffer
	producer       queue.Producer // underlying — buffering 데코레이터 아닌 raw Kafka producer
	checker        queue.BacklogChecker
	leader         LeaderLocker       // nil 허용 — 단일 인스턴스 모드
	retryScheduler bus.RetryScheduler // nil 허용 — fallback 으로 Redis 재적재
	groupID        string
	targetBacklog  int64
	drainBatch     int
	maxLen         int64 // RetryScheduler 부재 시 Redis 재적재 fallback 의 LTRIM cap
	interval       time.Duration
	checkTimeout   time.Duration
	log            *logger.Logger

	wg     sync.WaitGroup
	stopCh chan struct{}
	once   sync.Once
}

// BufferDrainerConfig 는 BufferDrainer 생성용 설정입니다.
type BufferDrainerConfig struct {
	// Interval — 매 tick 의 주기. 권장 30s.
	Interval time.Duration
	// TargetBacklog — 유지하려는 Kafka lag 상한. drain 후 backlog 가 이 값 이하로 유지되도록.
	// scheduler.BacklogThrottler 의 MaxBacklog (보통 5000) 의 60% 권장 (예: 3000).
	TargetBacklog int64
	// DrainBatch — 한 tick 당 priority 별로 최대 drain 할 메시지 수. 권장 100.
	DrainBatch int
	// MaxLen — RetryScheduler 부재 시 fallback 으로 Redis 재적재 시 buffer LIST 최대 길이.
	MaxLen int64
	// CheckTimeout — Backlog() 호출 한 번에 적용할 deadline. 0 이면 ctx 만 사용.
	CheckTimeout time.Duration
	// GroupID — Kafka consumer group ID (보통 queue.GroupCrawlerWorkers).
	GroupID string
	// Leader — 다중 인스턴스 leader election (이슈 #512). nil 이면 단일 인스턴스 모드.
	Leader LeaderLocker
	// RetryScheduler — publish 실패 시 Kafka 재시도 경로 (이슈 #512). nil 이면 Redis 재적재 fallback.
	RetryScheduler bus.RetryScheduler
}

// NewBufferDrainer 는 BufferDrainer 를 생성합니다.
//
// buffer / producer / checker / log 가 nil 이면 nil + error (silent crash 회피).
// cfg.Interval / DrainBatch / TargetBacklog 가 0 이하면 합리적 default 적용.
func NewBufferDrainer(
	buffer queue.JobBuffer,
	producer queue.Producer,
	checker queue.BacklogChecker,
	cfg BufferDrainerConfig,
	log *logger.Logger,
) (*BufferDrainer, error) {
	if buffer == nil {
		return nil, errors.New("buffer drainer: nil JobBuffer")
	}
	if producer == nil {
		return nil, errors.New("buffer drainer: nil underlying Producer")
	}
	if checker == nil {
		return nil, errors.New("buffer drainer: nil BacklogChecker")
	}
	if log == nil {
		return nil, errors.New("buffer drainer: nil logger")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.DrainBatch <= 0 {
		cfg.DrainBatch = 100
	}
	if cfg.TargetBacklog <= 0 {
		cfg.TargetBacklog = 3000
	}
	if cfg.GroupID == "" {
		cfg.GroupID = queue.GroupCrawlerWorkers
	}
	return &BufferDrainer{
		buffer:         buffer,
		producer:       producer,
		checker:        checker,
		leader:         cfg.Leader,
		retryScheduler: cfg.RetryScheduler,
		groupID:        cfg.GroupID,
		targetBacklog:  cfg.TargetBacklog,
		drainBatch:     cfg.DrainBatch,
		maxLen:         cfg.MaxLen,
		interval:       cfg.Interval,
		checkTimeout:   cfg.CheckTimeout,
		log:            log,
		stopCh:         make(chan struct{}),
	}, nil
}

// drainTargets 는 buffer label → Kafka topic 매핑입니다. high 는 buffer 사용 안 함.
var drainTargets = []struct {
	label string
	topic string
}{
	{label: "normal", topic: queue.TopicCrawlNormal},
	{label: "low", topic: queue.TopicCrawlLow},
}

// SetRetryScheduler 는 publish 실패 시 사용할 RetryScheduler 를 set 합니다 (이슈 #512).
// main.go 의 wiring 순서상 BufferDrainer 가 jobPublisher / retryScheduler 보다 먼저 생성되므로
// late binding 을 허용. Start 호출 전에 한 번만 set — 이후 변경은 race (atomic 미사용).
//
// nil set 은 명시적으로 허용 — RetryScheduler 비활성 (Redis 재적재 fallback) 의미.
func (d *BufferDrainer) SetRetryScheduler(rs bus.RetryScheduler) {
	d.retryScheduler = rs
}

// SetLeader 는 다중 인스턴스 leader election 용 LeaderLocker 를 set 합니다 (이슈 #512).
// Start 호출 전에 한 번만 set — late binding. nil set 시 단일 인스턴스 모드.
func (d *BufferDrainer) SetLeader(l LeaderLocker) {
	d.leader = l
}

// Start 는 background goroutine 을 띄워 주기적 drain 을 시작합니다.
// ctx 는 long-lived parent — cancel 되면 다음 tick 에 종료. Stop() 도 동등 효과.
func (d *BufferDrainer) Start(ctx context.Context) {
	d.wg.Add(1)
	go d.run(ctx)
	d.log.WithFields(map[string]interface{}{
		"interval":       d.interval.String(),
		"target_backlog": d.targetBacklog,
		"drain_batch":    d.drainBatch,
	}).Info("buffer drainer started")
}

// Stop 은 다음 tick 전에 drainer 를 정지합니다. 이미 진행 중인 drainOnce 는 완료까지 대기.
func (d *BufferDrainer) Stop() {
	d.once.Do(func() {
		close(d.stopCh)
	})
	d.wg.Wait()
	d.log.Info("buffer drainer stopped")
}

func (d *BufferDrainer) run(ctx context.Context) {
	defer d.wg.Done()
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	// 부팅 직후 1회 즉시 drain — buffer 에 이전 세션 잔존물이 있을 수 있음.
	d.drainAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.drainAll(ctx)
		}
	}
}

func (d *BufferDrainer) drainAll(ctx context.Context) {
	// Leader election (이슈 #512) — 다중 인스턴스 환경에서 한 인스턴스만 drain.
	// nil leader 면 단일 인스턴스 모드로 항상 진행. acquire 실패 (Redis 장애 등) 시 fail-closed —
	// 모든 인스턴스 skip 으로 target_backlog 초과 회피 (이슈 #512 본문 분석).
	if d.leader != nil {
		acquired, err := d.leader.TryAcquire(ctx)
		if err != nil {
			d.log.WithError(err).Warn("buffer drainer leader acquire failed, skipping cycle (fail-closed)")
			return
		}
		if !acquired {
			d.log.Debug("buffer drainer leader held by another instance, skipping cycle")
			return
		}
		// Release 는 본 cycle 완료 후 시도 — ctx cancel 됐어도 ownership 확인 후 정상 해제 가능하도록
		// WithoutCancel + 짧은 timeout. release 실패 시 TTL 만료로 자연 회수.
		defer func() {
			relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if relErr := d.leader.Release(relCtx); relErr != nil {
				d.log.WithError(relErr).Warn("buffer drainer leader release failed (lock will expire via TTL)")
			}
		}()
	}

	for _, tgt := range drainTargets {
		if err := d.drainOnce(ctx, tgt.label, tgt.topic); err != nil {
			// 개별 priority 실패가 다른 priority 의 drain 을 막지 않도록.
			d.log.WithFields(map[string]interface{}{
				"label": tgt.label,
				"topic": tgt.topic,
			}).WithError(err).Warn("buffer drain cycle failed (non-fatal)")
		}
	}
}

func (d *BufferDrainer) drainOnce(ctx context.Context, label, topic string) error {
	// 1) 현재 backlog 조회
	checkCtx := ctx
	if d.checkTimeout > 0 {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(ctx, d.checkTimeout)
		defer cancel()
	}
	backlog, err := d.checker.Backlog(checkCtx, topic, d.groupID)
	if err != nil {
		// backlog 조회 실패 → fail-closed (이번 cycle drain skip). throttle 과 반대 — 여기서
		// fail-open 하면 backlog 추정 없이 무제한 drain → Kafka 과부하 위험.
		return fmt.Errorf("backlog check for %s: %w", topic, err)
	}

	available := d.targetBacklog - backlog
	if available <= 0 {
		d.log.WithFields(map[string]interface{}{
			"label":          label,
			"topic":          topic,
			"backlog":        backlog,
			"target_backlog": d.targetBacklog,
		}).Debug("buffer drain skipped — target backlog reached")
		return nil
	}

	// 2) buffer 현재 길이 조회 — drain 할 양 결정
	bufLen, err := d.buffer.JobBufferLen(ctx, label)
	if err != nil {
		return fmt.Errorf("buffer len %s: %w", label, err)
	}
	if bufLen == 0 {
		return nil // idle — 정상
	}

	n := int(available)
	if d.drainBatch < n {
		n = d.drainBatch
	}
	if int(bufLen) < n {
		n = int(bufLen)
	}

	// 3) drain
	payloads, err := d.buffer.DrainJobs(ctx, label, n)
	if err != nil {
		return fmt.Errorf("drain %s: %w", label, err)
	}
	if len(payloads) == 0 {
		return nil
	}

	// 4) underlying Producer 로 publish
	msgs := make([]queue.Message, 0, len(payloads))
	for _, p := range payloads {
		msg, _, decErr := queue.DecodeBufferedMessage(p)
		if decErr != nil {
			d.log.WithError(decErr).Warn("buffer drain decode failed, dropping payload")
			continue
		}
		msgs = append(msgs, msg)
	}
	if len(msgs) == 0 {
		return nil
	}

	if pubErr := d.producer.PublishBatch(ctx, msgs); pubErr != nil {
		// publish 실패 처리 (이슈 #512):
		//   - retryScheduler 가 wiring 됐으면 → Kafka 재시도 경로 (즉시/지연 publish) 사용
		//     → fetcher worker 가 빠르게 pickup, 다음 drain tick (30s) 대기 회피
		//   - retryScheduler 가 nil 이면 → Redis 재적재 fallback (기존 동작)
		// shutdown 중 ctx cancel 시에도 복구 시도 — WithoutCancel + 5s timeout.
		d.log.WithFields(map[string]interface{}{
			"label": label,
			"topic": topic,
			"count": len(msgs),
		}).WithError(pubErr).Warn("buffer drain publish failed, dispatching to retry path")
		reCtx, reCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer reCancel()

		if d.retryScheduler != nil {
			d.dispatchToRetry(reCtx, msgs, label, topic, pubErr)
		} else {
			// fallback: Redis 재적재 (기존 PR #511 동작 보존)
			if reErr := d.buffer.EnqueueBatch(reCtx, label, payloads, d.maxLen); reErr != nil {
				d.log.WithFields(map[string]interface{}{
					"label": label,
					"count": len(payloads),
				}).WithError(reErr).Warn("re-enqueue after publish failure failed (data loss possible)")
			}
		}
		return fmt.Errorf("publish batch %s: %w", topic, pubErr)
	}

	d.log.WithFields(map[string]interface{}{
		"label":           label,
		"topic":           topic,
		"drained":         len(msgs),
		"backlog_before":  backlog,
		"target_backlog":  d.targetBacklog,
		"buffer_len_left": bufLen - int64(len(payloads)),
	}).Info("buffer drained to kafka")
	return nil
}

// dispatchToRetry 는 publish 실패한 메시지들을 bus.RetryScheduler 로 위임하여 Kafka 재시도 경로에
// 진입시킵니다 (이슈 #512). msg.Value 는 CrawlJob 의 JSON Marshal — Unmarshal 후 RetryScheduler.
// Enqueue 호출. RetryScheduler 가 즉시/지연 publish + retry-count 헤더 관리.
//
// 개별 메시지 처리 실패는 다른 메시지에 영향 X — 모두 시도 + 최종 warn 카운트 로깅.
// 본 dispatch 자체도 실패하는 케이스 (예: Kafka 전체 장애) 에서는 RetryScheduler 가 이미
// Kafka.WriteMessages 를 시도하므로 직전 PublishBatch 실패와 동일 사유 — 추가 fallback 없음
// (Redis 재적재로 가도 다음 tick 에 동일 Kafka 시도 반복). 운영자는 warn 누적으로 감지.
func (d *BufferDrainer) dispatchToRetry(ctx context.Context, msgs []queue.Message, label, topic string, pubErr error) {
	successCount := 0
	failCount := 0
	for _, msg := range msgs {
		job, err := core.UnmarshalCrawlJob(msg.Value)
		if err != nil {
			d.log.WithFields(map[string]interface{}{
				"label": label,
				"topic": topic,
			}).WithError(err).Warn("retry dispatch decode failed, dropping message")
			failCount++
			continue
		}
		if err := d.retryScheduler.Enqueue(ctx, job, pubErr); err != nil {
			d.log.WithFields(map[string]interface{}{
				"label":  label,
				"topic":  topic,
				"job_id": job.ID,
			}).WithError(err).Warn("retry dispatch enqueue failed")
			failCount++
			continue
		}
		successCount++
	}
	d.log.WithFields(map[string]interface{}{
		"label":     label,
		"topic":     topic,
		"succeeded": successCount,
		"failed":    failCount,
	}).Info("buffer drain publish failures dispatched to retry scheduler")
}
