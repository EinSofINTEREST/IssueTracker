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
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/urlguard"
)

// PriorityResolver는 CrawlJob의 우선순위를 결정하는 인터페이스입니다.
// worker.CompositeResolver 등이 이를 구현합니다.
type PriorityResolver interface {
	Resolve(job *core.CrawlJob) core.Priority
}

// URLCache 는 publish 직전에 URL 의 캐시 hit 여부를 확인하는 최소 인터페이스입니다.
// (이슈 #126)
//
// 의도적으로 작은 인터페이스 — Publisher 는 단지 "이 URL 을 보내도 되는가?" 만
// 알고 싶어합니다. worker 패키지의 URLCache 와 method signature 가 동일하지만
// publisher 가 worker 를 import 하지 않도록 별도 정의 — RedisURLCache 등 기존
// 구현체는 구조적 타이핑으로 그대로 만족합니다.
//
// 구현체는 goroutine-safe 해야 합니다.
type URLCache interface {
	Exists(ctx context.Context, url string) (bool, error)
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
// URL dedup (이슈 #126):
//   - SetURLCache 로 URLCache 를 설정하면 message build 직전에 cache hit URL 을 필터링
//   - TargetTypeCategory 는 dedup 미적용 (consumer-side 와 동일 규칙 — 카테고리는 매 주기
//     새 기사 추출이 목적)
//   - 캐시 조회 실패는 fail-open (해당 URL publish 진행) — Redis 일시 장애로 publish 가
//     멈추지 않도록
//   - 미설정 시 dedup 비활성 (기존 동작 유지)
type Publisher struct {
	producer queue.Producer
	resolver PriorityResolver
	gate     atomic.Pointer[urlguard.Gate]
	urlCache atomic.Pointer[urlCacheRef]
	log      *logger.Logger
}

// urlCacheRef 는 atomic.Pointer 가 인터페이스 값을 직접 저장하지 못하므로
// URLCache 인터페이스를 감싸 atomic 교체를 지원하는 wrapper 입니다.
type urlCacheRef struct {
	c URLCache
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

// SetURLCache 는 Publish 시 cache hit URL 사전 필터링에 사용할 URLCache 를 설정합니다.
// nil 전달 시 dedup 비활성 (기존 동작 유지). atomic 으로 race-safe 한 swap 보장.
func (p *Publisher) SetURLCache(c URLCache) {
	if c == nil {
		p.urlCache.Store(nil)
		return
	}
	p.urlCache.Store(&urlCacheRef{c: c})
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

	// URL dedup (이슈 #126): cache hit URL 사전 필터링
	// - TargetTypeCategory 는 dedup 미적용 (consumer-side 와 동일 규칙)
	// - 캐시 조회 실패는 fail-open (해당 URL 통과 + WARN 로그) — Redis 일시 장애로
	//   publish 가 멈추지 않도록
	if r := p.urlCache.Load(); r != nil && targetType != core.TargetTypeCategory {
		urls = p.filterCachedURLs(ctx, urls, crawlerName, r.c)
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

// filterCachedURLs 는 URLCache 를 조회하여 cache hit 인 URL 을 제외한 슬라이스를
// 반환합니다 (이슈 #126).
//
//   - cache hit  : DEBUG 로그 후 결과에서 제외 — 다운스트림 worker 가 cache hit 으로
//     skip 할 것을 미리 producer 단에서 걸러 Kafka 왕복 절약
//   - cache miss : 결과 슬라이스에 포함
//   - 조회 실패  : fail-open (결과 슬라이스에 포함) + WARN 로그 — Redis 일시 장애가
//     publish 를 영구 차단하지 않도록
//
// 결과 슬라이스는 입력과 다른 underlying array 로 새로 할당됩니다 (입력 mutate 없음).
func (p *Publisher) filterCachedURLs(ctx context.Context, urls []string, crawlerName string, cache URLCache) []string {
	out := make([]string, 0, len(urls))
	for _, url := range urls {
		cached, err := cache.Exists(ctx, url)
		if err != nil {
			p.log.WithFields(map[string]interface{}{
				"crawler": crawlerName,
				"stage":   "publisher",
				"url":     url,
			}).WithError(err).Warn("url cache check failed, allowing publish")
			out = append(out, url)
			continue
		}
		if cached {
			p.log.WithFields(map[string]interface{}{
				"crawler": crawlerName,
				"stage":   "publisher",
				"url":     url,
			}).Debug("url already cached, skipping publish")
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
