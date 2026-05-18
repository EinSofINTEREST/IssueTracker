package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// BufferDrainer 는 Redis JobBuffer 에 적재된 normal/low priority crawl 메시지를
// Kafka backlog 가 임계 미만일 때 점진적으로 underlying Producer 로 publish 합니다 (이슈 #510).
//
// 동작 (주기적 tick):
//  1. priority 별로 Kafka topic 의 현재 backlog (consumer-group lag) 를 BacklogChecker 로 조회
//  2. available = targetBacklog - currentBacklog
//  3. n := min(available, drainBatch, JobBuffer.JobBufferLen)
//  4. JobBuffer.DrainJobs(label, n) → underlying.PublishBatch
//  5. publish 실패 시 drained payload 들을 다시 EnqueueJob 으로 재적재 (순서 보존 X — fail-safe)
//
// Stop 호출 시 다음 tick 전에 종료. 이미 drain 중인 사이클은 완료까지 대기 (graceful).
type BufferDrainer struct {
	buffer        queue.JobBuffer
	producer      queue.Producer // underlying — buffering 데코레이터 아닌 raw Kafka producer
	checker       queue.BacklogChecker
	groupID       string
	targetBacklog int64
	drainBatch    int
	maxLen        int64 // 재적재 시 LTRIM cap (BufferingProducer 와 동일 값)
	interval      time.Duration
	checkTimeout  time.Duration
	log           *logger.Logger

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
	// MaxLen — 재적재 시 buffer LIST 최대 길이 (BufferingProducer 와 동일 값 권장).
	MaxLen int64
	// CheckTimeout — Backlog() 호출 한 번에 적용할 deadline. 0 이면 ctx 만 사용.
	CheckTimeout time.Duration
	// GroupID — Kafka consumer group ID (보통 queue.GroupCrawlerWorkers).
	GroupID string
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
		buffer:        buffer,
		producer:      producer,
		checker:       checker,
		groupID:       cfg.GroupID,
		targetBacklog: cfg.TargetBacklog,
		drainBatch:    cfg.DrainBatch,
		maxLen:        cfg.MaxLen,
		interval:      cfg.Interval,
		checkTimeout:  cfg.CheckTimeout,
		log:           log,
		stopCh:        make(chan struct{}),
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
		// publish 실패 → 재적재 (순서 보존 X). EnqueueBatch 로 1 RTT — N개 EnqueueJob 회피
		// (gemini PR #511 피드백).
		d.log.WithFields(map[string]interface{}{
			"label": label,
			"topic": topic,
			"count": len(msgs),
		}).WithError(pubErr).Warn("buffer drain publish failed, re-enqueueing payloads")
		if reErr := d.buffer.EnqueueBatch(ctx, label, payloads, d.maxLen); reErr != nil {
			d.log.WithFields(map[string]interface{}{
				"label": label,
				"count": len(payloads),
			}).WithError(reErr).Warn("re-enqueue after publish failure failed (data loss possible)")
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
