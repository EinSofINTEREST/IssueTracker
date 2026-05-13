// Package workerpool 은 Kafka consumer 기반 stage worker 의 공용 harness 를 제공합니다 (이슈 #404).
//
// 세 stage worker (fetcher / parser / validate) 가 동일하게 반복하던 lifecycle 코드
// (poll → dispatch → commit → graceful shutdown) 를 단일 source-of-truth 로 통합 — 향후
// 신규 stage 추가 시 boilerplate 비용 ↓ + 한 곳 수정이 세 stage 에 일관 반영.
//
// 설계 원칙 — minimal harness:
//   - harness 가 owns:    poll / dispatch / worker_id 주입 / graceful shutdown / commit-with-drain
//   - handler 가 owns:    메시지 unmarshal / 비즈니스 로직 / DLQ 라우팅 / requeue 결정 / StageGate
//
// stage-specific 결정 (DLQ vs requeue, validation 분기, CB / retry scheduler 통합) 은 handler
// 에서 자유롭게 — harness 는 강제 정책 미보유. 공통 helper (CommitWithDrain) 만 노출.
//
// 마이그레이션 sub (Sub 2-4) 에서 stage 별 handler 가 본 harness 위에서 stage 핵심만 담는
// 형태로 재구성됩니다.
package workerpool

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

// DefaultDrainTimeout 은 graceful shutdown 으로 ctx 가 canceled 된 뒤 commit / publish 를
// 한 번 더 시도할 때 사용하는 별도 context 의 fallback 타임아웃입니다.
// at-least-once 시맨틱 보장 — 세 stage 의 기존 drainTimeout 과 동일 값.
const DefaultDrainTimeout = 5 * time.Second

// Handler 는 stage 별 메시지 처리 책임을 가지는 인터페이스입니다.
//
// 구현체는 stage-specific 로직 (unmarshal / validation / DLQ / requeue / commit) 의 모든
// 권한을 갖습니다. harness 는 자체 commit 정책을 강제하지 않으며, handler 가 ConsumerPool.Commit
// 을 통해 명시적으로 commit 합니다.
//
// goroutine-safe 해야 함 — workerCount > 1 시 동시 호출.
type Handler interface {
	Handle(ctx context.Context, msg *queue.Message)
}

// Config 는 ConsumerPool 생성 옵션입니다.
type Config struct {
	// Consumer 는 Kafka consumer (bus.Consumer = queue.Consumer alias).
	Consumer bus.Consumer
	// Handler 는 메시지 처리기.
	Handler Handler
	// WorkerCount 는 동시 처리 goroutine 수. 0 이하는 1 로 보정.
	WorkerCount int
	// DrainTimeout 은 shutdown 시 commit retry timeout. 0 이하면 DefaultDrainTimeout.
	DrainTimeout time.Duration
	// Log 는 lifecycle 로그용. nil 이면 noop logger.
	Log *logger.Logger
	// Name 은 로그 식별자 (예: "fetcher-high" / "parser" / "validator"). 빈 문자열 허용.
	Name string
}

// ConsumerPool 은 generic Kafka consumer worker pool harness 입니다.
//
// 종료 순서 보장:
//  1. pollCancel → poll goroutine 즉시 ctx.Err() 받고 종료
//  2. pollWg.Wait — poll 이 jobs 채널 send 를 더 이상 시도 안 함을 보장
//  3. close(jobs) — worker goroutine 의 range loop 종료 신호
//  4. wg.Wait (ctx 또는 drainTimeout 으로 시간 cap)
//  5. consumer.Close()
//
// 이 순서는 send-on-closed-channel panic 을 구조적으로 방지.
type ConsumerPool struct {
	cfg        Config
	jobs       chan *queue.Message
	wg         sync.WaitGroup
	pollWg     sync.WaitGroup
	pollCancel context.CancelFunc
	// stopOnce 는 Stop 다중 호출 안전성 보장 (gemini PR #413 피드백 — close on closed
	// channel panic 회피). 2회차 이후 Stop 은 nil 반환 — idempotent.
	stopOnce sync.Once
	// stopErr 는 첫 Stop 호출의 에러를 보존 — 2회차 호출은 동일 에러를 참조하지 않고 nil 반환.
	stopErr error
}

// New 는 새 ConsumerPool 을 생성합니다.
//
// cfg.Consumer / cfg.Handler 는 nil 불허 — Start 전 panic 으로 fail-fast.
// WorkerCount / DrainTimeout 은 0 이하면 default 적용.
func New(cfg Config) *ConsumerPool {
	if cfg.Consumer == nil {
		panic("workerpool.New: Consumer must not be nil")
	}
	if cfg.Handler == nil {
		panic("workerpool.New: Handler must not be nil")
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 1
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = DefaultDrainTimeout
	}
	return &ConsumerPool{
		cfg: cfg,
		// 버퍼 worker_count*2 — polling 과 처리 사이 지연 흡수 (기존 세 stage 동일 값).
		jobs: make(chan *queue.Message, cfg.WorkerCount*2),
	}
}

// Start 는 poll + worker goroutine 들을 기동합니다 (non-blocking).
//
// 각 worker goroutine 은 0..WorkerCount-1 의 worker_id 가 ctx 에 첨부되어 handler 에 전달됩니다.
// fetcher chromedp pool 의 per-worker Semaphore lookup 등 worker 자원 격리에 활용됩니다.
func (p *ConsumerPool) Start(ctx context.Context) {
	for i := 0; i < p.cfg.WorkerCount; i++ {
		workerID := i
		p.wg.Add(1)
		go p.worker(ctx, workerID)
	}

	// poll goroutine 의 ctx 는 별도 cancel — Stop 시 poll 만 먼저 중단시켜 send-on-closed
	// 회피. wg.Add 는 goroutine 시작 전 — "Add called concurrently with Wait" 패닉 방지.
	pollCtx, cancel := context.WithCancel(ctx)
	p.pollCancel = cancel
	p.pollWg.Add(1)
	go p.poll(pollCtx)
}

// Stop 은 worker pool 을 정상 종료합니다 — 다중 호출 안전 (gemini PR #413).
//
// ctx 는 worker drain timeout 으로 활용 — 만료 시 force close 로 진행 (commit 안 된 메시지는
// 다음 기동에서 재소비). consumer.Close 는 항상 호출됩니다.
//
// 반환 정책:
//   - drain graceful + consumer.Close OK → nil
//   - drain timeout (ctx canceled 으로 worker 미완) → errors.Join(ctx.Err(), closeErr)
//   - drain OK + consumer.Close 실패 → closeErr
//
// 2회차 이후 호출은 nil 반환 (idempotent).
func (p *ConsumerPool) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() {
		p.stopErr = p.stopImpl(ctx)
	})
	return p.stopErr
}

func (p *ConsumerPool) stopImpl(ctx context.Context) error {
	log := p.logger(ctx)

	// 1. poll 중단 — 새 메시지 fetch 안 함 + ctx.Err() 로 즉시 반환
	if p.pollCancel != nil {
		p.pollCancel()
	}
	p.pollWg.Wait()

	// 2. poll 종료 확정 후 jobs close — send-on-closed-channel panic 회피
	close(p.jobs)

	// 3. worker drain — ctx 만료 시 force progress (deferred consumer.Close 까지 진행)
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	var drainErr error
	select {
	case <-done:
		log.Info("worker pool drained gracefully")
	case <-ctx.Done():
		// ctx.Err() 를 단일 진실 공급원 (SoT) 으로 — 호출자가 errors.Is 로 분기 가능 (gemini PR #413).
		drainErr = fmt.Errorf("worker drain timeout: %w", ctx.Err())
		log.Warn("worker pool shutdown timeout, forcing close")
	}

	// 4. consumer close (drainErr 있어도 항상 호출 — Kafka 리소스 정리 보장)
	closeErr := p.cfg.Consumer.Close()
	if drainErr != nil {
		return errors.Join(drainErr, closeErr) // closeErr 가 nil 이면 errors.Join 이 drainErr 만 반환
	}
	return closeErr
}

// poll 은 Kafka 에서 메시지를 한 건씩 fetch 하여 jobs 채널에 dispatch 합니다.
//
// ctx cancel 시 즉시 반환 — Stop 의 pollCancel 호출이 본 goroutine 을 unblock 합니다.
// FetchMessage 에러는 ctx canceled 가 아닌 경우만 ERROR — 운영 모니터링에서 정상 종료 흐름의
// 오탐을 제거.
func (p *ConsumerPool) poll(ctx context.Context) {
	defer p.pollWg.Done()

	log := p.logger(ctx)

	for {
		msg, err := p.cfg.Consumer.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.WithError(err).Error("failed to receive kafka message")
			continue
		}

		select {
		case p.jobs <- msg:
		case <-ctx.Done():
			return
		}
	}
}

// worker 는 jobs 채널에서 메시지를 받아 handler 에 위임합니다.
//
// worker_id 를 ctx 에 첨부하여 handler 가 per-worker 자원 lookup (chromedp 의 worker_id 별
// Semaphore 등) 가능. range loop 는 jobs 채널 close 시 자연 종료.
func (p *ConsumerPool) worker(ctx context.Context, workerID int) {
	defer p.wg.Done()

	ctx = core.WithWorkerID(ctx, workerID)

	for msg := range p.jobs {
		p.cfg.Handler.Handle(ctx, msg)
	}
}

// Commit 은 메시지 offset 을 commit 합니다 — drain context 로 ctx canceled 시 재시도.
//
// handler 가 명시적으로 호출 — harness 는 자체 commit 정책 미보유 (stage 별 DLQ / requeue 결정
// 후 handler 가 적절히 commit). 호출자는 commit 실패를 로깅 / metric 으로 반영.
//
// graceful shutdown 으로 ctx 가 canceled 된 경우 별도 context.WithoutCancel + DrainTimeout 으로
// 한 번 더 시도 — at-least-once 정확도 향상.
func (p *ConsumerPool) Commit(ctx context.Context, msg *queue.Message) error {
	return CommitWithDrain(ctx, p.cfg.Consumer, msg, p.cfg.DrainTimeout)
}

// CommitWithDrain 은 메시지 commit + drain context retry 를 수행하는 stateless 헬퍼입니다.
//
// 첫 commit 시도 → ctx canceled 면 context.WithoutCancel + drainTimeout 으로 재시도 →
// errors.Join 으로 최초 cancel 과 retry err 모두 보존. ConsumerPool 외부 (단위 테스트 / 다른
// 콜러) 에서도 사용 가능.
func CommitWithDrain(ctx context.Context, consumer bus.Consumer, msg *queue.Message, drainTimeout time.Duration) error {
	err := consumer.CommitMessages(ctx, msg)
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.Canceled) {
		return fmt.Errorf("commit offset: %w", err)
	}

	// drain retry — cancellation 분리 + drainTimeout 으로 시간 cap.
	if drainTimeout <= 0 {
		drainTimeout = DefaultDrainTimeout
	}
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
	defer cancel()

	if retryErr := consumer.CommitMessages(drainCtx, msg); retryErr == nil {
		return nil
	} else {
		// errors.Join — 호출자의 errors.Is(err, context.Canceled) 분기 안정성 보존.
		return fmt.Errorf("commit offset (drain retry failed): %w", errors.Join(err, retryErr))
	}
}

// logger 는 cfg.Log 가 있으면 그대로 반환, 없으면 ctx logger 로 fallback.
// Name 이 설정되어 있으면 stage 식별자 필드 첨부.
func (p *ConsumerPool) logger(ctx context.Context) *logger.Logger {
	log := p.cfg.Log
	if log == nil {
		log = logger.FromContext(ctx)
	}
	if p.cfg.Name != "" {
		log = log.WithField("worker_pool", p.cfg.Name)
	}
	return log
}
