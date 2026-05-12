package publisher

import (
	"context"
	"fmt"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/links"
	"issuetracker/pkg/queue"
)

// PublishChained 는 크롤된 페이지에서 발견된 URL 목록을 CrawlJob 으로 변환하여
// 우선순위에 맞는 Kafka crawl 토픽에 일괄 발행합니다 (구 Publish — 이슈 #386).
//
// 발행 흐름:
//  1. URL 정규화 (Normalizer 주입 시)
//  2. urlguard.Gate 사전 필터링 (Gate 주입 시)
//  3. PipelineGuard / IngestionLock 으로 진입 marker 획득 (둘 중 하나 주입 시)
//  4. PriorityResolver.Resolve 로 priority 결정
//  5. queue.Message 빌드 + Producer.PublishBatch
//
// 단건 순차 호출 대신 PublishBatch 를 사용하여 Kafka 왕복을 1회로 줄입니다.
func (p *Publisher) PublishChained(
	ctx context.Context,
	crawlerName string,
	urls []string,
	targetType core.TargetType,
	timeout time.Duration,
) error {
	if len(urls) == 0 {
		return nil
	}

	// URL 정규화: publish 직전 단일 책임으로 정규화
	// - Ingestion Lock 키 / Kafka payload / 다운스트림 dedup 모두 동일 정규형 사용
	// - 정규화 실패한 URL 은 통과 (fail-open) — 정규화 실패가 fetch 가능성을 차단하지 않도록
	// - Normalizer 미설정 시 원본 그대로
	if n := p.normalizer.Load(); n != nil {
		urls = p.normalizeURLs(urls, n, crawlerName)
		if len(urls) == 0 {
			return nil
		}
	}

	// URL 가드: 차단된 URL 을 사전 필터링
	// Gate 가 자체 WARN 로그 + url/reason/crawler/stage 필드 자동 부착
	if g := p.gate.Load(); g != nil {
		urls = g.Filter(urls, map[string]interface{}{
			"crawler": crawlerName,
			"stage":   "publisher",
		})
		if len(urls) == 0 {
			return nil
		}
	}

	// Pipeline Guard / Ingestion Lock:
	// - PipelineGuard 우선 — target type 별 TTL 정책 (Article 24h / Category 단명)
	// - IngestionLock fallback — Article 만 적용 (Category 우회) — backward compat
	// - 둘 다 미설정 시 dedup 비활성
	// - 조회 실패는 fail-open — Redis 일시 장애가 publish 를 영구 차단하지 않도록
	if gr := p.guard.Load(); gr != nil {
		urls = p.acquireViaGuard(ctx, urls, crawlerName, gr.g, targetType)
		if len(urls) == 0 {
			return nil
		}
	} else if r := p.lock.Load(); r != nil && targetType != core.TargetTypeCategory {
		urls = p.acquireIngestion(ctx, urls, crawlerName, r.l)
		if len(urls) == 0 {
			return nil
		}
	}

	msgs, err := p.buildJobMessages(crawlerName, urls, targetType, timeout)
	if err != nil {
		return err
	}

	if err := p.producer.PublishBatch(ctx, msgs); err != nil {
		return fmt.Errorf("batch publish %d jobs for crawler %s: %w", len(msgs), crawlerName, err)
	}

	p.log.WithFields(map[string]interface{}{
		"crawler":   crawlerName,
		"job_count": len(msgs),
	}).Debug("chained jobs batch published to kafka")

	return nil
}

// buildJobMessages 는 url 목록을 CrawlJob 으로 변환하여 priority resolve 후 Kafka Message
// 슬라이스로 반환합니다 (CodeRabbit PR #394 피드백 — PublishChained 함수 분리).
//
// MaxRetries 는 publisher.DefaultMaxRetries 상수 사용.
func (p *Publisher) buildJobMessages(
	crawlerName string,
	urls []string,
	targetType core.TargetType,
	timeout time.Duration,
) ([]queue.Message, error) {
	msgs := make([]queue.Message, 0, len(urls))
	for _, url := range urls {
		job := &core.CrawlJob{
			ID:          newJobID(),
			CrawlerName: crawlerName,
			Target: core.Target{
				URL:  url,
				Type: targetType,
			},
			ScheduledAt: time.Now(),
			Timeout:     timeout,
			MaxRetries:  DefaultMaxRetries,
		}

		job.Priority = p.resolver.Resolve(job)

		msg, err := p.buildMessage(job)
		if err != nil {
			return nil, fmt.Errorf("build message for %s: %w", url, err)
		}

		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// normalizeURLs 는 입력 URL 슬라이스를 정규화하여 새 슬라이스로 반환합니다.
//
// 정규화 실패한 URL 은 원본을 그대로 통과 (fail-open) + WARN 로그 — 정규화 자체가
// fetch 가능성을 차단하지 않도록. 정규화 결과가 빈 문자열이면 결과에서 제외.
//
// 성능: crawler/stage sub-logger 를 1회 생성 후 재사용.
func (p *Publisher) normalizeURLs(urls []string, n *links.Normalizer, crawlerName string) []string {
	out := make([]string, 0, len(urls))
	l := p.log.WithFields(map[string]interface{}{
		"crawler": crawlerName,
		"stage":   "publisher",
	})

	for _, url := range urls {
		normalized, err := n.Normalize(url)
		if err != nil {
			l.WithField("url", url).WithError(err).Warn("url normalize failed, using original")
			out = append(out, url)
			continue
		}
		if normalized == "" {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

// acquireViaGuard 는 PipelineGuard 로 target type 별 TTL 정책을 적용하여 진입 marker 를 잡습니다.
//
// 동작 정책은 acquireIngestion 과 동일 — fail-open / ctx 취소 / 정규화 가정 등.
// 차이점: lock.Acquire 대신 guard.CheckAndAcquire(targetType) 호출 — Category 는 단명 TTL 적용.
func (p *Publisher) acquireViaGuard(ctx context.Context, urls []string, crawlerName string, guard PipelineGuard, targetType core.TargetType) []string {
	out := make([]string, 0, len(urls))
	l := p.log.WithFields(map[string]interface{}{
		"crawler":     crawlerName,
		"stage":       "publisher",
		"target_type": string(targetType),
	})

	for i, url := range urls {
		if err := ctx.Err(); err != nil {
			l.WithError(err).Warn("context cancelled during pipeline guard acquire, allowing remaining URLs")
			return append(out, urls[i:]...)
		}

		acquired, err := guard.CheckAndAcquire(ctx, url, targetType)
		if err != nil {
			l.WithField("url", url).WithError(err).Warn("pipeline guard check failed, allowing publish")
			out = append(out, url)
			continue
		}
		if !acquired {
			l.WithField("url", url).Debug("url already in pipeline, skipping publish")
			continue
		}
		out = append(out, url)
	}
	return out
}

// acquireIngestion 은 IngestionLock 으로 atomic SETNX 시도 후 marker 를 잡은 URL 만
// 반환합니다.
//
//   - acquired=true  : 신규 진입 marker 획득 — 결과 슬라이스에 포함
//   - acquired=false : 이미 다른 publisher 또는 재배달이 marker 점유 — DEBUG 로그 후 제외
//   - 조회 실패      : fail-open (결과 슬라이스에 포함) + WARN 로그 — Redis 일시 장애가
//     publish 를 영구 차단하지 않도록
//   - ctx 취소       : 즉시 종료하고 남은 URL 은 fail-open 으로 그대로 통과 — 셧다운 중
//     무의미한 lock 호출/WARN 누적 회피. 후속 PublishBatch 가 ctx 에러로 자연 실패.
//
// 결과 슬라이스는 입력과 다른 underlying array 로 새로 할당됩니다 (입력 mutate 없음).
//
// 성능: crawler/stage sub-logger 를 루프 외부에서 1회 생성하여 재사용.
//
// Deprecated: SetPipelineGuard 사용 시 acquireViaGuard 가 우선 — 본 메소드는
// guard 미주입 환경의 backward compat fallback.
func (p *Publisher) acquireIngestion(ctx context.Context, urls []string, crawlerName string, lock IngestionLock) []string {
	out := make([]string, 0, len(urls))
	l := p.log.WithFields(map[string]interface{}{
		"crawler": crawlerName,
		"stage":   "publisher",
	})

	for i, url := range urls {
		if err := ctx.Err(); err != nil {
			l.WithError(err).Warn("context cancelled during ingestion lock acquire, allowing remaining URLs")
			return append(out, urls[i:]...)
		}

		acquired, err := lock.Acquire(ctx, url)
		if err != nil {
			l.WithField("url", url).WithError(err).Warn("ingestion lock acquire failed, allowing publish")
			out = append(out, url)
			continue
		}
		if !acquired {
			l.WithField("url", url).Debug("url already in pipeline, skipping publish")
			continue
		}
		out = append(out, url)
	}
	return out
}
