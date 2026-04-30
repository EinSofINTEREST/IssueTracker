package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	// processingLockKeyPrefix 는 단계별 URL 단위 처리 lock 의 키 접두사입니다 (이슈 #178).
	// 형식: "processing:<stage>:url:<sha256(normalized_url)>" — Redis 운영자가 grep / SCAN 으로
	// 다른 namespace (ingestion:url:..., lock:job:... 등) 와 구분 가능.
	processingLockKeyPrefix = "processing:"

	// DefaultProcessingLockTTL 은 ProcessingLock 의 기본 TTL 입니다.
	// 단계별 처리 최대 시간보다 충분히 길게 설정하여 정상 처리 중 만료를 방지합니다.
	// 단계별 다른 TTL 이 필요하면 NewRedisProcessingLock 에 다른 값을 주입한 별도 instance 사용.
	DefaultProcessingLockTTL = 10 * time.Minute
)

// ProcessingLock 은 파이프라인 단계별 URL 중복 처리를 방지하는 분산 락 인터페이스입니다 (이슈 #178).
//
// \"각 프로세스마다 작업 중인 URL 을 다른 동일 프로세스 단계의 워커가 건들지 못하도록 막는 dedup.\"
//
// 구 명칭 JobLocker — 본질이 임의 식별자에 대한 atomic SETNX 라 일반화. fetcher / parser / validator
// 가 동일 인터페이스를 stage 만 달리하여 사용 (key helper: ProcessingKey).
//
// 구현체는 goroutine-safe 해야 합니다.
type ProcessingLock interface {
	// Acquire 는 key 에 대한 락을 획득합니다.
	// 이미 다른 worker 가 처리 중이면 (false, nil).
	// 오류 발생 시 (false, error) — 호출자는 graceful degrade 정책 적용.
	Acquire(ctx context.Context, key string) (bool, error)

	// Release 는 key 에 대한 락을 해제합니다.
	// 정상 종료 / 실패 모두 호출 (defer 패턴) — TTL 만료 대기 회피.
	Release(ctx context.Context, key string) error
}

// ProcessingLock stage 표준 상수 — 호출자가 stage 를 직접 string literal 로 쓰지 않고
// 이 상수를 통해 일관된 키를 만들도록 강제 (typo 방지 + 검색 용이).
//
// 외부 패키지 (internal/parser/worker, internal/processor/validate) 도 import 후 사용.
const (
	StageFetcher   = "fetcher"
	StageParser    = "parser"
	StageValidator = "validator"
)

// ProcessingKey 는 (stage, normalized_url) 페어로 ProcessingLock 의 Redis 키를 생성합니다 (이슈 #178).
//
// 호출자 책임: url 은 pkg/links.Normalizer 로 정규화된 상태로 전달 — 동일 컨텐츠를 가리키는
// 두 URL 이 다른 키를 갖지 않도록.
//
// stage 표준 값 (worker 패키지 내부에서 stageFetcher / stageParser / stageValidator 상수 사용):
//   - "fetcher" — crawler worker (raw HTML fetch)
//   - "parser"  — parser worker (rule.Parser.ParsePage / ParseLinks)
//   - "validator" — validate worker (Content 검증)
//
// 외부 패키지 (parser/worker, processor/validate) 는 worker 패키지에서 export 된 상수를 사용해야 함 —
// StageFetcher / StageParser / StageValidator 로 export 가능하나 본 PR 에선 worker 측에서만 사용.
func ProcessingKey(stage, url string) string {
	h := sha256.Sum256([]byte(url))
	return fmt.Sprintf("%s%s:url:%s", processingLockKeyPrefix, stage, hex.EncodeToString(h[:]))
}

// RedisProcessingLock 은 Redis SET NX EX 기반 ProcessingLock 구현체입니다.
type RedisProcessingLock struct {
	locker redisLocker
	ttl    time.Duration
}

// redisLocker 는 Redis 락 조작을 추상화하는 내부 인터페이스입니다.
// pkg/redis.Client 의 메서드 집합과 일치하며 테스트에서 mock 으로 교체됩니다.
type redisLocker interface {
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
}

// NewRedisProcessingLock 는 RedisProcessingLock 을 생성합니다.
// ttl 이 0 이하이면 DefaultProcessingLockTTL 사용.
func NewRedisProcessingLock(locker redisLocker, ttl time.Duration) *RedisProcessingLock {
	if ttl <= 0 {
		ttl = DefaultProcessingLockTTL
	}
	return &RedisProcessingLock{locker: locker, ttl: ttl}
}

// Acquire 는 key 의 처리 marker 를 SET NX EX 로 atomic 시도합니다.
//
// nil receiver / nil locker 보호 — RedisIngestionLock 과 동일 패턴.
func (l *RedisProcessingLock) Acquire(ctx context.Context, key string) (bool, error) {
	if l == nil || l.locker == nil {
		return false, fmt.Errorf("processing lock locker is nil")
	}
	return l.locker.AcquireLock(ctx, key, l.ttl)
}

// Release 는 key 의 marker 를 즉시 제거합니다.
func (l *RedisProcessingLock) Release(ctx context.Context, key string) error {
	if l == nil || l.locker == nil {
		return fmt.Errorf("processing lock locker is nil")
	}
	return l.locker.ReleaseLock(ctx, key)
}

// NoopProcessingLock 은 lock 을 사용하지 않는 no-op 구현체입니다.
// Redis 부재 환경 (단일 인스턴스, 테스트) 에서 fallback 으로 사용 — 항상 acquired=true.
//
// **운영 영향**: Noop 사용 시 단계별 dedup 비활성 — Kafka rebalance / 재배달 시 같은 URL 의
// 동시 처리 가능. 단일 인스턴스 + 단일 worker 환경에선 race 자체가 거의 없으므로 문제 없음.
type NoopProcessingLock struct{}

// Acquire always returns acquired=true (Noop).
func (NoopProcessingLock) Acquire(_ context.Context, _ string) (bool, error) { return true, nil }

// Release is a no-op.
func (NoopProcessingLock) Release(_ context.Context, _ string) error { return nil }

// 컴파일 타임 인터페이스 만족 검증.
var (
	_ ProcessingLock = (*RedisProcessingLock)(nil)
	_ ProcessingLock = NoopProcessingLock{}
)
