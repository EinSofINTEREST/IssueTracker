// Package publisher는 크롤러가 페이지에서 발견한 URL을 다음 CrawlJob으로 연결하는
// 체이닝 발행 컴포넌트를 제공합니다.
//
// 역할 분리:
//   - Scheduler  : 등록된 소스의 시드 Job만 생성 (internal/scheduler)
//   - Publisher  : 크롤 결과에서 발견된 URL을 다음 Job으로 연결 (이 패키지)
package publisher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/links"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/urlguard"
)

// PriorityResolver는 CrawlJob의 우선순위를 결정하는 인터페이스입니다.
// worker.CompositeResolver 등이 이를 구현합니다.
type PriorityResolver interface {
	Resolve(job *core.CrawlJob) core.Priority
}

// IngestionLock 은 publish 직전에 URL 의 파이프라인 진입 marker 를 atomic 으로 set
// 하는 최소 인터페이스입니다 (이슈 #178).
//
// 의도적으로 작은 인터페이스 — Publisher 는 단지 "이 URL 의 진입 슬롯을 잡을 수 있는가?"
// 만 알고 싶어합니다. worker 패키지의 IngestionLock 와 method signature 가 동일하지만
// publisher 가 worker 를 import 하지 않도록 별도 정의 — RedisIngestionLock 등 기존
// 구현체는 구조적 타이핑으로 그대로 만족합니다.
//
// 구현체는 goroutine-safe 해야 합니다.
type IngestionLock interface {
	Acquire(ctx context.Context, url string) (bool, error)
}

// Publisher는 크롤된 페이지에서 발견된 URL을 새 CrawlJob으로 변환하여
// 우선순위에 맞는 Kafka crawl 토픽에 발행합니다.
//
// URL 가드 (이슈 #119):
//   - SetGate 로 urlguard.Gate 를 설정하면 PublishBatch 직전에 urls 슬라이스를 필터링
//   - 차단된 URL 은 발행에서 제외 (Gate 가 자체 WARN 로그)
//   - 미설정 시 가드 비활성 (기존 동작 유지)
//   - atomic.Pointer 로 race-safe 한 lock-free 설정/조회 — 워커 동시 실행 중 변경에도 race 없음
//
// URL dedup — Ingestion Lock (이슈 #178, 이슈 #126 의 단일 책임화):
//   - SetNormalizer 로 pkg/links.Normalizer 를 주입하면 publish 직전 모든 URL 정규화
//     (정규화된 URL 이 Ingestion Lock 키 / Kafka payload / 다운스트림 dedup 모두에 일관)
//   - SetIngestionLock 으로 IngestionLock 을 설정하면 message build 직전에 atomic SETNX
//     — 이미 진입한 URL 은 publish 에서 제외 (다운스트림 worker 에서도 다시 차단 불필요)
//   - TargetTypeCategory 는 lock 미적용 (카테고리 페이지는 매 주기 새 기사 추출이 목적)
//   - lock 조회 실패는 fail-open (해당 URL publish 진행) — Redis 일시 장애로 publish 가
//     멈추지 않도록
//   - 미설정 시 dedup 비활성 (기존 동작 유지)
type Publisher struct {
	producer   queue.Producer
	resolver   PriorityResolver
	gate       atomic.Pointer[urlguard.Gate]
	normalizer atomic.Pointer[links.Normalizer]
	lock       atomic.Pointer[ingestionLockRef]
	log        *logger.Logger
}

// ingestionLockRef 는 atomic.Pointer 가 인터페이스 값을 직접 저장하지 못하므로
// IngestionLock 인터페이스를 감싸 atomic 교체를 지원하는 wrapper 입니다.
type ingestionLockRef struct {
	l IngestionLock
}

// New는 새 Publisher를 생성합니다.
func New(producer queue.Producer, resolver PriorityResolver, log *logger.Logger) *Publisher {
	return &Publisher{
		producer: producer,
		resolver: resolver,
		log:      log,
	}
}

// SetGate 는 Publish 시 urls 사전 필터링에 사용할 urlguard.Gate 를 설정합니다.
// 미설정(nil) 시 가드 비활성 — 모든 urls 가 그대로 publish 됩니다.
//
// 동시성: atomic.Pointer 기반 lock-free 설정/조회 — Publish 동시 실행 중 변경에도 race-safe.
func (p *Publisher) SetGate(g *urlguard.Gate) {
	p.gate.Store(g)
}

// SetNormalizer 는 Publish 직전 URL 정규화에 사용할 Normalizer 를 설정합니다.
// nil 전달 시 정규화 비활성 (URL 원본 그대로 사용). atomic 으로 race-safe 한 swap 보장.
//
// 정규화는 Ingestion Lock 키 / Kafka payload / 다운스트림 dedup 모두에 동일하게 적용되도록
// Publisher 단에서 단일 책임으로 수행 (이슈 #178).
func (p *Publisher) SetNormalizer(n *links.Normalizer) {
	p.normalizer.Store(n)
}

// SetIngestionLock 은 Publish 시 atomic SETNX 로 진입 marker 를 잡을 IngestionLock 을
// 설정합니다 (이슈 #178). nil 전달 시 dedup 비활성 (기존 동작 유지).
//
// atomic 으로 race-safe 한 swap 보장.
func (p *Publisher) SetIngestionLock(l IngestionLock) {
	if l == nil {
		p.lock.Store(nil)
		return
	}
	p.lock.Store(&ingestionLockRef{l: l})
}

// Publish는 발견된 URL 목록으로 CrawlJob을 생성하고 한 번의 배치 요청으로 Kafka에 발행합니다.
// 단건 순차 호출 대신 PublishBatch를 사용하여 Kafka 왕복을 1회로 줄입니다.
func (p *Publisher) Publish(
	ctx context.Context,
	crawlerName string,
	urls []string,
	targetType core.TargetType,
	timeout time.Duration,
) error {
	if len(urls) == 0 {
		return nil
	}

	// URL 정규화 (이슈 #178): publish 직전 단일 책임으로 정규화
	// - Ingestion Lock 키 / Kafka payload / 다운스트림 dedup 모두 동일 정규형 사용
	// - 정규화 실패한 URL 은 통과 (fail-open) — 정규화 실패가 fetch 가능성을 차단하지 않도록
	// - Normalizer 미설정 시 원본 그대로
	if n := p.normalizer.Load(); n != nil {
		urls = p.normalizeURLs(urls, n, crawlerName)
	}

	// URL 가드 (이슈 #119): 차단된 URL 을 사전 필터링
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

	// Ingestion Lock (이슈 #178): atomic SETNX 로 진입 marker 잡기
	// - TargetTypeCategory 는 lock 미적용 (카테고리는 매 주기 새 기사 추출이 목적)
	// - lock 조회 실패는 fail-open (해당 URL 통과 + WARN 로그) — Redis 일시 장애로
	//   publish 가 멈추지 않도록
	if r := p.lock.Load(); r != nil && targetType != core.TargetTypeCategory {
		urls = p.acquireIngestion(ctx, urls, crawlerName, r.l)
		if len(urls) == 0 {
			return nil
		}
	}

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
			MaxRetries:  3,
		}

		job.Priority = p.resolver.Resolve(job)

		msg, err := p.buildMessage(job)
		if err != nil {
			return fmt.Errorf("build message for %s: %w", url, err)
		}

		msgs = append(msgs, msg)
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

// normalizeURLs 는 입력 URL 슬라이스를 정규화하여 새 슬라이스로 반환합니다 (이슈 #178).
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

// acquireIngestion 은 IngestionLock 으로 atomic SETNX 시도 후 marker 를 잡은 URL 만
// 반환합니다 (이슈 #178).
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

// buildMessage는 CrawlJob을 Kafka Message로 변환합니다.
func (p *Publisher) buildMessage(job *core.CrawlJob) (queue.Message, error) {
	data, err := job.Marshal()
	if err != nil {
		return queue.Message{}, fmt.Errorf("marshal job %s: %w", job.ID, err)
	}

	return queue.Message{
		Topic: crawlTopic(job.Priority),
		Key:   []byte(job.ID),
		Value: data,
		Headers: map[string]string{
			"crawler":  job.CrawlerName,
			"priority": fmt.Sprintf("%d", int(job.Priority)),
		},
	}, nil
}

// crawlTopic은 Priority에 대응하는 Kafka crawl 토픽 이름을 반환합니다.
func crawlTopic(p core.Priority) string {
	switch p {
	case core.PriorityHigh:
		return queue.TopicCrawlHigh
	case core.PriorityLow:
		return queue.TopicCrawlLow
	default:
		return queue.TopicCrawlNormal
	}
}

// newJobID는 crypto/rand 기반의 고유 Job ID(32자 hex)를 생성합니다.
func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
