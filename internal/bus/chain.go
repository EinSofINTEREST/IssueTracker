package bus

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
// MaxRetries 는 worker.DefaultMaxRetries 상수 사용.
func (p *Publisher) buildJobMessages(
	crawlerName string,
	urls []string,
	targetType core.TargetType,
	timeout time.Duration,
) ([]queue.Message, error) {
	msgs := make([]queue.Message, 0, len(urls))
	// gemini PR #394 피드백 — time.Now() loop 외부 1회 호출 (system call 감소).
	// 같은 batch 의 모든 job 은 동일 발견 시점이므로 ScheduledAt 동기.
	now := time.Now()
	for _, url := range urls {
		job := &core.CrawlJob{
			ID:          newJobID(),
			CrawlerName: crawlerName,
			Target: core.Target{
				URL:  url,
				Type: targetType,
			},
			ScheduledAt: now,
			Timeout:     timeout,
			MaxRetries:  DefaultMaxRetries,
		}

		// 이슈 #391 — resolver chain 통과는 buildMessage 가 흡수 (모든 PublishX 일관성).
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

// acquireFunc 는 단일 URL 의 진입 marker 획득 시도를 추상화합니다.
// acquireViaGuard / acquireIngestion 공통 흐름 (filterByAcquire) 에서 사용 — gemini PR #394
// 피드백 (중복 제거).
type acquireFunc func(ctx context.Context, url string) (acquired bool, err error)

// filterByAcquire 는 ctx 취소 / fail-open / 로깅 공통 패턴으로 acquire 시도하여 marker 를
// 잡은 URL 만 반환합니다.
//
//   - acquired=true  : 신규 진입 marker 획득 → 결과 포함
//   - acquired=false : 이미 다른 publisher 가 점유 → DEBUG 로그 + 제외
//   - err            : fail-open (결과 포함) + WARN 로그 — Redis 일시 장애 시 publish 영구 차단 회피
//   - ctx 취소       : 즉시 종료 + 남은 URL fail-open 통과 — 후속 PublishBatch 가 ctx 에러로 자연 실패
//
// 결과 슬라이스는 입력과 다른 underlying array 로 새 할당 (입력 mutate 없음).
//
// failOpenMsg / cancelledMsg 는 호출자가 acquire 의미에 맞게 지정 — "pipeline guard" / "ingestion lock" 구분.
// extraFields 는 acquireViaGuard 의 target_type 같은 추가 컨텍스트.
func (p *Publisher) filterByAcquire(
	ctx context.Context,
	urls []string,
	crawlerName string,
	op acquireFunc,
	failOpenMsg, cancelledMsg string,
	extraFields map[string]interface{},
) []string {
	out := make([]string, 0, len(urls))
	fields := map[string]interface{}{
		"crawler": crawlerName,
		"stage":   "publisher",
	}
	for k, v := range extraFields {
		fields[k] = v
	}
	l := p.log.WithFields(fields)

	for i, url := range urls {
		if err := ctx.Err(); err != nil {
			l.WithError(err).Warn(cancelledMsg)
			return append(out, urls[i:]...)
		}

		acquired, err := op(ctx, url)
		if err != nil {
			l.WithField("url", url).WithError(err).Warn(failOpenMsg)
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

// acquireViaGuard 는 PipelineGuard 로 target type 별 TTL 정책을 적용하여 진입 marker 를 잡습니다.
//
// 동작 정책은 acquireIngestion 과 동일 — fail-open / ctx 취소 / 정규화 가정 등.
// 차이점: lock.Acquire 대신 guard.CheckAndAcquire(targetType) 호출 — Category 는 단명 TTL 적용.
func (p *Publisher) acquireViaGuard(ctx context.Context, urls []string, crawlerName string, guard PipelineGuard, targetType core.TargetType) []string {
	op := func(ctx context.Context, url string) (bool, error) {
		return guard.CheckAndAcquire(ctx, url, targetType)
	}
	return p.filterByAcquire(
		ctx, urls, crawlerName, op,
		"pipeline guard check failed, allowing publish",
		"context cancelled during pipeline guard acquire, allowing remaining URLs",
		map[string]interface{}{"target_type": string(targetType)},
	)
}

// acquireIngestion 은 IngestionLock 으로 atomic SETNX 시도 후 marker 를 잡은 URL 만
// 반환합니다.
//
// Deprecated: SetPipelineGuard 사용 시 acquireViaGuard 가 우선 — 본 메소드는
// guard 미주입 환경의 backward compat fallback.
func (p *Publisher) acquireIngestion(ctx context.Context, urls []string, crawlerName string, lock IngestionLock) []string {
	return p.filterByAcquire(
		ctx, urls, crawlerName, lock.Acquire,
		"ingestion lock acquire failed, allowing publish",
		"context cancelled during ingestion lock acquire, allowing remaining URLs",
		nil,
	)
}
