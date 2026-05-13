package worker

import (
	"context"
	"errors"
	"fmt"

	"issuetracker/internal/processor/fetcher/core"
)

// ErrPublishSkipped 는 PipelineGuard 가 "이미 in-pipeline" 으로 판단해 publish 를 건너뛴 경우
// PublishSeed 가 반환하는 sentinel error 입니다 (이슈 #387 — 구 scheduler.ErrEmitSkipped).
//
// 호출자는 errors.Is(err, worker.ErrPublishSkipped) 로 분기하여 "failed to publish" /
// "scheduled" 로그를 모두 생략 — 실제로 발행되지 않은 job 이 발행된 것처럼 보이는 misleading
// 로그 회피.
var ErrPublishSkipped = errors.New("publish skipped — url already in pipeline")

// SeedPublisher 는 시드 CrawlJob 발행 책임을 정의하는 인터페이스입니다
// (이슈 #396 — 메타 #385 의 Kafka I/O 단일 책임 원칙에 따라 publisher 패키지에서 계약을 정의).
//
// worker.Publisher 가 본 인터페이스를 만족하며, 외부 모듈은 본 인터페이스를 통해 시드
// 발행 기능을 주입받아 사용합니다. 인터페이스 정의를 publisher 측에 두는 이유:
//   - Kafka I/O 책임 = publisher 단일 진실 원천 — 시그니처 / 계약 변경 시 publisher 측만 갱신
//   - 다른 sub (UpgradePublisher / RetryPublisher 등) 도 동일 원칙 적용 — 정합 일관
//
// SeedPublisher dispatches seed CrawlJobs to the appropriate Kafka crawl topic.
// Callers are only responsible for creating jobs; publish routing, guarding, and
// normalization are owned by the publisher implementation.
type SeedPublisher interface {
	PublishSeed(ctx context.Context, job *core.CrawlJob) error
}

// PublishSeed 는 scheduler 가 생성한 시드 CrawlJob 을 Kafka crawl 토픽에 발행합니다 (이슈 #387).
//
// 흐름 (구 scheduler.JobEmitter.Emit 이 본 메소드로 이동):
//  1. Normalizer 가 있으면 URL 정규화 (guard 키 일관성)
//  2. PipelineGuard 가 있으면 CheckAndAcquire 로 진입 marker 획득
//     - acquired=false → ErrPublishSkipped 반환 (이미 in-pipeline)
//     - 조회 실패 → fail-open (allow publish + warn)
//  3. job.Marshal + producer.Publish (job.Priority → topic)
//  4. publish 실패 시 guard.Release (acquired 였던 경우만)
//
// 시드 발행은 entry.Priority 가 사전 결정된 채로 호출되므로 chain.go 의 PriorityResolver 통과
// 안 함 — 본 메소드는 단일 job 발행 + guard 책임만 흡수. resolver chain 통합은 메타 #385
// Sub 6 에서.
func (p *Publisher) PublishSeed(ctx context.Context, job *core.CrawlJob) error {
	// gemini PR #395 피드백 — exported method 의 nil 입력 방어. caller 가 nil 전달 시
	// 즉시 fail-fast (panic 회피 + 디버깅 가시성).
	if job == nil {
		return errors.New("publish seed: nil job")
	}

	// guard 키 일관성: chained publish 도 동일 normalizer 적용 후 CheckAndAcquire 함.
	// 시드에서도 같은 정규형으로 marker 잡아야 동일 URL 이 두 입구에서 같은 키 사용.
	// 정규화 실패는 fail-open — 원본으로 fallback.
	guardURL := job.Target.URL
	if n := p.normalizer.Load(); n != nil {
		if normalized, nerr := n.Normalize(guardURL); nerr == nil && normalized != "" {
			guardURL = normalized
		}
	}
	guardAcquired := false // publish 실패 시 release 호출 여부 추적

	if gr := p.guard.Load(); gr != nil {
		acquired, gerr := gr.g.CheckAndAcquire(ctx, guardURL, job.Target.Type)
		if gerr != nil {
			p.log.WithFields(map[string]interface{}{
				"job_id":  job.ID,
				"crawler": job.CrawlerName,
				"url":     job.Target.URL,
			}).WithError(gerr).Warn("pipeline guard check failed, allowing publish")
		} else if !acquired {
			p.log.WithFields(map[string]interface{}{
				"job_id":      job.ID,
				"crawler":     job.CrawlerName,
				"url":         job.Target.URL,
				"target_type": string(job.Target.Type),
			}).Debug("seed publish skipped — url already in pipeline")
			return ErrPublishSkipped
		} else {
			guardAcquired = true
		}
	}

	msg, err := p.buildMessage(job)
	if err != nil {
		// gemini PR #395 피드백 — release 는 ctx cancel 와 무관하게 수행되어야 함
		// (shutdown 중에도 marker 해제 필수 — 안 그러면 다음 cycle silent skip).
		p.releaseGuardOnFailure(context.WithoutCancel(ctx), guardURL, guardAcquired, job)
		return err
	}

	if err := p.producer.Publish(ctx, msg); err != nil {
		// publish 실패 시 marker 즉시 해제 — 다음 retry 가 false acquired 로 silent skip 되지 않도록.
		// ctx cancel 로 publish 실패 시에도 release 는 수행 (context.WithoutCancel).
		p.releaseGuardOnFailure(context.WithoutCancel(ctx), guardURL, guardAcquired, job)
		return fmt.Errorf("publish seed job %s to %s: %w", job.ID, msg.Topic, err)
	}

	p.log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"crawler":  job.CrawlerName,
		"url":      job.Target.URL,
		"priority": int(job.Priority),
		"topic":    msg.Topic,
	}).Debug("seed job emitted to kafka")

	return nil
}

// releaseGuardOnFailure 는 CheckAndAcquire 가 marker 를 잡았으나 후속 marshal/publish 가
// 실패한 경우 marker 를 즉시 해제합니다.
//
// guardAcquired=false 이거나 guard=nil 이면 noop. Release 실패는 non-fatal — TTL fallback 으로
// 자연 해제.
//
// Copilot PR #395 피드백 — 로그 메시지를 일반화 (구 "after publish error" 는 marshal 실패
// 분기에서도 호출되어 모호) → "after marshal/publish failure" 로 정정.
func (p *Publisher) releaseGuardOnFailure(ctx context.Context, url string, guardAcquired bool, job *core.CrawlJob) {
	if !guardAcquired {
		return
	}
	gr := p.guard.Load()
	if gr == nil {
		return
	}
	if rerr := gr.g.Release(ctx, url); rerr != nil {
		p.log.WithFields(map[string]interface{}{
			"job_id": job.ID,
			"url":    url,
		}).WithError(rerr).Warn("pipeline guard release failed after marshal/publish failure (TTL fallback applies)")
	}
}
