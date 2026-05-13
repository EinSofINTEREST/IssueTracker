// Package bus 는 Kafka crawl 토픽 발행 책임을 단일 hub 로 통합합니다 (이슈 #385).
//
// 역할 (메타 #385 — Publisher 통합 모듈화):
//   - PublishChained : 크롤된 페이지에서 발견된 URL 을 다음 CrawlJob 으로 연결 (chain.go)
//   - PublishSeed    : scheduler 의 시드 entry 발행 (Sub 2 — pending)
//   - PublishRetry   : 워커 실패 시 재시도 발행 (Sub 4 — pending)
//   - PublishUpgrade : auto-upgrade (goquery → chromedp) republish (Sub 3 — pending)
//
// 외부 facade 단일화 — caller 는 *Publisher 의 메소드만 사용하면 됨. 내부 file 분리:
//   - worker.go : facade struct + 생성자 + 공통 Kafka helpers (buildMessage / CrawlTopic / newJobID)
//   - chain.go     : PublishChained 메소드 + 정규화 / guard / ingestion lock helper
//   - guard.go     : IngestionLock / PipelineGuard / atomic wrapper + Set* setters
//
// 의존 관계 (이슈 #385 책임 분리 원칙):
//   - 본 패키지 = Kafka I/O + 라우팅 (priority resolver) + guard/lock 책임
//   - caller 의 stage 핵심 로직 (parsing rule / validation / fetch decision) 은 본 패키지 의존성 없음
package bus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
// 호출자는 publisher 가 제공하는 인스턴스나 외부 wiring 에서 queue.NewConsumer 로 생성한
// *KafkaConsumer 를 그대로 사용. 본 패키지가 별도 factory 메소드를 제공하기 전까지는
// queue.NewConsumer wiring 을 caller 측에서 직접 수행.
type Consumer = queue.Consumer

// Message 는 Kafka 메시지 구조체의 publisher-측 별칭입니다 (이슈 #390 피드백 — gemini).
//
// Consumer 별칭과 마찬가지로 다운스트림 모듈이 queue 패키지에 직접 의존하지 않고 publisher
// API 만으로 Forward 호출 시 메시지 구성을 완성할 수 있도록 별칭화. queue.Message 와 동일.
type Message = queue.Message

// DefaultMaxRetries 는 PublishX 메소드들이 생성하는 CrawlJob 의 기본 재시도 횟수입니다
// (CodeRabbit PR #394 피드백 — magic number 상수화).
const DefaultMaxRetries = 3

// PriorityResolver 는 CrawlJob 의 우선순위를 결정하는 인터페이스입니다.
// 구현체 — ExplicitPriorityResolver / SourcePriorityResolver / RuleBasedPriorityResolver /
// DefaultPriorityResolver / CompositeResolver (chain) — 모두 publisher 패키지의
// [resolver.go](resolver.go) 에 위치 (이슈 #391 — 메타 #385 Sub 6).
//
// 모든 PublishX 메소드의 priority 결정 단일 진입점 — 운영자가 한 곳에만 resolver 룰 추가하면
// seed / chained / retry / upgrade 모든 경로에 일관 적용. ExplicitPriorityResolver 를 chain
// 1순위 로 등록하면 발행자가 사전 명시한 priority 가 보존됨.
type PriorityResolver interface {
	Resolve(job *core.CrawlJob) core.Priority
}

// Publisher 는 Kafka crawl 토픽 발행 단일 facade 입니다 (이슈 #385).
//
// 필드는 모두 atomic.Pointer 로 lock-free 설정/조회 — Set* setter 가 동시 publish 와 race-safe.
//
// 사용 흐름:
//
//	pub := bus.New(producer, resolver, log)
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

// Forward 는 미리 구성된 Message 를 내부 producer 로 그대로 발행합니다 (이슈 #390).
//
// 사용처 — fetcher/worker 가 Kafka I/O 책임을 publisher 로 위임하면서도 worker-특수
// 메시지 (normalized contentRef / DLQ 등) 의 구성 / 라우팅은 worker 측에 잔존하는 경우의
// thin pass-through. publisher 가 자체 Marshal/Topic 결정을 책임지는 PublishX 와 달리 본
// 메소드는 호출자가 완성된 Message 를 만들어 전달합니다.
//
// nil guard — coderabbit PR #400 피드백. 본 메소드가 retry / worker hot path 에서 호출되므로
// p 또는 p.producer 가 nil 일 때 panic 대신 에러 반환 (silent crash 보다 명시적 fail).
//
// Kafka I/O 자체는 publisher 가 단일 출처 — 호출자는 queue.Producer 를 직접 보유하지 않음.
func (p *Publisher) Forward(ctx context.Context, msg Message) error {
	if p == nil {
		return errors.New("publisher: Forward called on nil *Publisher")
	}
	if p.producer == nil {
		return errors.New("publisher: producer not wired")
	}
	return p.producer.Publish(ctx, msg)
}

// PublishJob 은 CrawlJob 을 marshal 하여 우선순위 토픽으로 발행합니다 (이슈 #390 피드백 — gemini).
//
// 구 manager.Publish 가 직접 수행하던 marshal / 토픽 결정 / 헤더 구성 로직을 publisher 측
// buildMessage 헬퍼로 일원화하여 코드 중복 제거. 호출자는 priority 가 미리 결정된 job 을
// 전달합니다 (priority resolver chain 통합은 Sub 6 에서).
func (p *Publisher) PublishJob(ctx context.Context, job *core.CrawlJob) error {
	if p == nil {
		return errors.New("publisher: PublishJob called on nil *Publisher")
	}
	if job == nil {
		return errors.New("publisher: PublishJob called with nil job")
	}
	msg, err := p.buildMessage(job)
	if err != nil {
		return err
	}
	return p.Forward(ctx, msg)
}

// buildMessage 는 CrawlJob 을 Kafka Message 로 변환합니다.
//
// 이슈 #391 — 메타 #385 Sub 6: resolver chain 통과를 본 헬퍼에 흡수. 모든 PublishX
// (Chained / Seed / Job / Retry / Redis retry republish) 가 buildMessage 를 거치므로
// priority 결정이 단일 출처. 발행자가 job.Priority 를 사전 명시한 경우, chain 1순위로
// 등록된 ExplicitPriorityResolver 가 그 값을 통과시켜 explicit 우선이 보존됩니다.
//
// resolver 가 nil 인 경우 (테스트 등) 는 기존 job.Priority 를 그대로 사용 — fail-safe.
//
// 부작용 회피 (gemini PR #409 피드백) — 외부에서 주입된 *job 의 Priority 를 직접 수정하지
// 않도록 local 복사본의 Priority 만 갱신. 호출자 (예: PoolManager.Publish) 가 원본 job 의
// Priority 변경을 기대하지 않더라도 안전. CrawlJob 은 작은 struct 이라 복사 비용 무시 가능.
func (p *Publisher) buildMessage(job *core.CrawlJob) (queue.Message, error) {
	j := *job
	if p.resolver != nil {
		j.Priority = p.resolver.Resolve(&j)
	}

	data, err := j.Marshal()
	if err != nil {
		return queue.Message{}, fmt.Errorf("marshal job %s: %w", j.ID, err)
	}

	return queue.Message{
		Topic: CrawlTopic(j.Priority),
		Key:   []byte(j.ID),
		Value: data,
		Headers: map[string]string{
			"crawler":  j.CrawlerName,
			"priority": fmt.Sprintf("%d", int(j.Priority)),
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
