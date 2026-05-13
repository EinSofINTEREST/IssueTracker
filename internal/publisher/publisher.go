// Package publisher 는 Kafka crawl 토픽 발행 책임을 단일 hub 로 통합합니다 (이슈 #385).
//
// 역할 (메타 #385 — Publisher 통합 모듈화):
//   - PublishChained : 크롤된 페이지에서 발견된 URL 을 다음 CrawlJob 으로 연결 (chain.go)
//   - PublishSeed    : scheduler 의 시드 entry 발행 (Sub 2 — pending)
//   - PublishRetry   : 워커 실패 시 재시도 발행 (Sub 4 — pending)
//   - PublishUpgrade : auto-upgrade (goquery → chromedp) republish (Sub 3 — pending)
//
// 외부 facade 단일화 — caller 는 *Publisher 의 메소드만 사용하면 됨. 내부 file 분리:
//   - publisher.go : facade struct + 생성자 + 공통 Kafka helpers (buildMessage / CrawlTopic / newJobID)
//   - chain.go     : PublishChained 메소드 + 정규화 / guard / ingestion lock helper
//   - guard.go     : IngestionLock / PipelineGuard / atomic wrapper + Set* setters
//
// 의존 관계 (이슈 #385 책임 분리 원칙):
//   - 본 패키지 = Kafka I/O + 라우팅 (priority resolver) + guard/lock 책임
//   - caller 의 stage 핵심 로직 (parsing rule / validation / fetch decision) 은 본 패키지 의존성 없음
package publisher

import (
	"context"
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

// Consumer 는 Kafka 메시지 소비 인터페이스의 publisher-측 별칭입니다 (이슈 #390).
//
// fetcher/worker 등 다운스트림 모듈이 queue 패키지에 직접 의존하지 않도록 — Kafka I/O
// 단일 책임 원칙 (메타 #385) 의 일환. queue.Consumer 와 100% 동일 시그니처 (type alias).
//
// 본 인터페이스를 만족하는 인스턴스는 publisher 가 SubscribeCrawlTopic 등 factory 메소드로
// 제공하거나, 외부 wiring 에서 queue.NewConsumer 로 생성한 *KafkaConsumer 를 그대로 사용 가능.
type Consumer = queue.Consumer

// DefaultMaxRetries 는 PublishX 메소드들이 생성하는 CrawlJob 의 기본 재시도 횟수입니다
// (CodeRabbit PR #394 피드백 — magic number 상수화).
const DefaultMaxRetries = 3

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

// Forward 는 미리 구성된 queue.Message 를 내부 producer 로 그대로 발행합니다 (이슈 #390).
//
// 사용처 — fetcher/worker 가 Kafka I/O 책임을 publisher 로 위임하면서도 worker-특수
// 메시지 (normalized contentRef / DLQ 등) 의 구성 / 라우팅은 worker 측에 잔존하는 경우의
// thin pass-through. publisher 가 자체 Marshal/Topic 결정을 책임지는 PublishX 와 달리 본
// 메소드는 호출자가 완성된 Message 를 만들어 전달합니다.
//
// Kafka I/O 자체는 publisher 가 단일 출처 — 호출자는 queue.Producer 를 직접 보유하지 않음.
func (p *Publisher) Forward(ctx context.Context, msg queue.Message) error {
	return p.producer.Publish(ctx, msg)
}

// buildMessage 는 CrawlJob 을 Kafka Message 로 변환합니다.
func (p *Publisher) buildMessage(job *core.CrawlJob) (queue.Message, error) {
	data, err := job.Marshal()
	if err != nil {
		return queue.Message{}, fmt.Errorf("marshal job %s: %w", job.ID, err)
	}

	return queue.Message{
		Topic: CrawlTopic(job.Priority),
		Key:   []byte(job.ID),
		Value: data,
		Headers: map[string]string{
			"crawler":  job.CrawlerName,
			"priority": fmt.Sprintf("%d", int(job.Priority)),
		},
	}, nil
}

// CrawlTopic 은 Priority 에 대응하는 Kafka crawl 토픽 이름을 반환합니다 (이슈 #389
// 피드백 — Kafka I/O 단일 책임 원칙에 따라 publisher 가 priority → topic 매핑의
// 유일한 출처. worker.topicForPriority / scheduler.crawlTopic 중복은 본 함수로 통합).
func CrawlTopic(p core.Priority) string {
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
//
// rand.Read 실패는 매우 드물지만 발생 시 all-zero ID → Kafka partition 충돌 / 다운스트림
// dedup 깨짐 → 데이터 정합성 심각. 운영 fail-fast 가 silent 데이터 손상보다 안전 (gemini
// security-medium PR #394 피드백).
func newJobID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("publisher: crypto/rand failure generating job ID: %w", err))
	}
	return hex.EncodeToString(b)
}
