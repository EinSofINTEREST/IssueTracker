package worker

import (
	"context"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/links"
	"issuetracker/pkg/urlguard"
)

// IngestionLock 은 publish 직전에 URL 의 파이프라인 진입 marker 를 atomic 으로 set
// 하는 최소 인터페이스입니다.
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

// PipelineGuard 는 publish 진입 시 URL 의 pipeline membership 을 target type 별 정책으로 체크합니다.
//
// publisher 가 internal/locks 를 직접 import 하지 않도록 별도 정의 — 구조적 타이핑으로
// locks.PipelineGuard 가 그대로 만족.
//
// Release: CheckAndAcquire 가 marker 를 잡았으나 후속 producer.Publish 가 실패한 경우 marker 를
// 즉시 해제 — 다음 retry 가 silent skip 으로 잃어버리지 않도록. 본 메소드는 시드 발행에서
// 사용 (이슈 #387). chained 발행은 batch 라 release 단위가 모호하여 적용 안 함.
type PipelineGuard interface {
	CheckAndAcquire(ctx context.Context, url string, targetType core.TargetType) (bool, error)
	Release(ctx context.Context, url string) error
}

// ingestionLockRef 는 atomic.Pointer 가 인터페이스 값을 직접 저장하지 못하므로
// IngestionLock 인터페이스를 감싸 atomic 교체를 지원하는 wrapper 입니다.
type ingestionLockRef struct {
	l IngestionLock
}

// guardRef 는 PipelineGuard 의 atomic 교체용 wrapper 입니다.
type guardRef struct {
	g PipelineGuard
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
// Publisher 단에서 단일 책임으로 수행.
func (p *Publisher) SetNormalizer(n *links.Normalizer) {
	p.normalizer.Store(n)
}

// SetIngestionLock 은 Publish 시 atomic SETNX 로 진입 marker 를 잡을 IngestionLock 을
// 설정합니다. nil 전달 시 dedup 비활성 (기존 동작 유지).
//
// atomic 으로 race-safe 한 swap 보장.
//
// Deprecated: SetPipelineGuard 사용 권장 — target type 별 TTL 정책 적용.
// 본 메소드는 backward compat 로 유지 — guard 미설정 시 fallback 으로 사용됨.
func (p *Publisher) SetIngestionLock(l IngestionLock) {
	if l == nil {
		p.lock.Store(nil)
		return
	}
	p.lock.Store(&ingestionLockRef{l: l})
}

// SetPipelineGuard 는 publish 진입 시 target type 별 정책으로 marker 를 잡을 PipelineGuard 를
// 설정합니다.
//
// guard 가 설정되어 있으면 IngestionLock 보다 우선 — 모든 target type 에 적용 (Article 24h,
// Category 단명 TTL). nil 전달 시 가드 비활성 — IngestionLock fallback (있으면).
func (p *Publisher) SetPipelineGuard(g PipelineGuard) {
	if g == nil {
		p.guard.Store(nil)
		return
	}
	p.guard.Store(&guardRef{g: g})
}
