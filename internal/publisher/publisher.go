// Package publisher 는 Kafka crawl 토픽 발행 책임을 단일 hub 로 통합합니다 (이슈 #385).
//
// 역할 (메타 #385 — Publisher 통합 모듈화):
//   - PublishChained : 크롤된 페이지에서 발견된 URL 을 다음 CrawlJob 으로 연결 (chain.go)
//   - PublishSeed    : scheduler 의 시드 entry 발행 (Sub 2 — pending)
//   - PublishRetry   : 워커 실패 시 재시도 발행 (Sub 4 — pending)
//   - PublishUpgrade : auto-upgrade (goquery → chromedp) republish (Sub 3 — pending)
//
// 외부 facade 단일화 — caller 는 *Publisher 의 메소드만 사용하면 됨. 내부 file 분리:
//   - publisher.go : facade struct + 생성자 + 공통 Kafka helpers (buildMessage / crawlTopic / newJobID)
//   - chain.go     : PublishChained 메소드 + 정규화 / guard / ingestion lock helper
//   - guard.go     : IngestionLock / PipelineGuard / atomic wrapper + Set* setters
//
// 의존 관계 (이슈 #385 책임 분리 원칙):
//   - 본 패키지 = Kafka I/O + 라우팅 (priority resolver) + guard/lock 책임
//   - caller 의 stage 핵심 로직 (parsing rule / validation / fetch decision) 은 본 패키지 의존성 없음
package publisher

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/links"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/urlguard"
)

// PriorityResolver 는 CrawlJob 의 우선순위를 결정하는 인터페이스입니다.
// worker.CompositeResolver 등이 이를 구현합니다.
//
// 메타 #385 Sub 6 에서 본 인터페이스 및 chain 구현이 publisher 측으로 이동될 예정 —
// 그 시점에 본 인터페이스가 모든 PublishX 메소드의 priority 결정 단일 진입점이 됩니다.
type PriorityResolver interface {
	Resolve(job *core.CrawlJob) core.Priority
}

// Publisher 는 Kafka crawl 토픽 발행 단일 facade 입니다 (이슈 #385).
//
// 필드는 모두 atomic.Pointer 로 lock-free 설정/조회 — Set* setter 가 동시 publish 와 race-safe.
//
// 사용 흐름:
//
//	pub := publisher.New(producer, resolver, log)
//	pub.SetNormalizer(...)        // 선택
//	pub.SetPipelineGuard(...)     // 선택 (또는 SetIngestionLock fallback)
//	pub.SetGate(...)              // 선택
//	pub.PublishChained(ctx, ...)  // chained URL 발행
type Publisher struct {
	producer   queue.Producer
	resolver   PriorityResolver
	gate       atomic.Pointer[urlguard.Gate]
	normalizer atomic.Pointer[links.Normalizer]
	lock       atomic.Pointer[ingestionLockRef]
	guard      atomic.Pointer[guardRef]
	log        *logger.Logger
}

// New 는 새 Publisher 를 생성합니다.
func New(producer queue.Producer, resolver PriorityResolver, log *logger.Logger) *Publisher {
	return &Publisher{
		producer: producer,
		resolver: resolver,
		log:      log,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 공통 Kafka helpers (모든 PublishX 메소드가 공유)
// ─────────────────────────────────────────────────────────────────────────────

// buildMessage 는 CrawlJob 을 Kafka Message 로 변환합니다.
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

// crawlTopic 은 Priority 에 대응하는 Kafka crawl 토픽 이름을 반환합니다.
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

// newJobID 는 crypto/rand 기반의 고유 Job ID (32자 hex) 를 생성합니다.
func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
