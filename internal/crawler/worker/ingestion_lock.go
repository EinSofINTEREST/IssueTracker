package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	// ingestionKeyPrefix 는 파이프라인 진입 marker 키의 접두사입니다.
	// Redis 운영자가 grep / SCAN 으로 진입 marker 만 식별할 수 있도록 namespace 분리.
	ingestionKeyPrefix = "ingestion:url:"

	// DefaultIngestionLockTTL 은 Ingestion Lock 의 기본 TTL 입니다.
	// 파이프라인 전체 통과 예상 시간 (publish → fetch → parse → validate → ...) 보다
	// 충분히 길게 — 24h 가 default. 환경변수로 운영 환경별 override 권장.
	DefaultIngestionLockTTL = 24 * time.Hour
)

// IngestionLock 은 URL 이 파이프라인에 진입한 상태를 marker 로 표시합니다 (이슈 #178).
//
// 의도:
//
//	"현재 파이프라인 내 어느 공간에서든 처리 단계에 들어온 URL 은 다시 fetch 하지 않도록 막는 dedup."
//
// Acquire 가 성공한 URL 은 (TTL 만료 전까지) 다른 publish 진입을 차단합니다.
// 정상 흐름에서는 명시 release 없이 TTL 자연 만료로 회수 — 운영자가 강제 재크롤 시 Invalidate.
//
// 구현체는 goroutine-safe 해야 합니다.
type IngestionLock interface {
	// Acquire 는 URL 의 파이프라인 진입 marker 를 atomic 으로 set 시도합니다.
	// 이미 marker 가 있으면 acquired=false (다른 publisher 또는 재배달).
	// 신규 set 이면 acquired=true — 호출자가 publish 진행.
	//
	// 호출자는 url 을 정규화 (pkg/links.Normalizer) 후 전달해야 — 동일 컨텐츠를 가리키는
	// 두 URL 이 다른 marker 를 갖지 않도록.
	Acquire(ctx context.Context, url string) (acquired bool, err error)

	// Invalidate 는 명시적으로 URL marker 를 제거합니다 — 운영자 강제 재크롤 시.
	// 정상 흐름에선 호출하지 않음 (TTL 자연 만료).
	Invalidate(ctx context.Context, url string) error
}

// RedisIngestionLock 은 Redis SET NX EX 기반 IngestionLock 구현체입니다.
//
// 키 형식: "ingestion:url:<sha256(normalized_url)>"
//   - sha256 으로 키 길이를 64자 고정 — 긴 URL (수백자) 도 안전
//   - prefix 분리 — Redis 운영자가 다른 namespace (lock:job:..., 향후 processing:...) 와
//     구분 가능
type RedisIngestionLock struct {
	locker redisIngestionLocker
	ttl    time.Duration
}

// redisIngestionLocker 는 Redis 락 조작을 추상화하는 내부 인터페이스입니다.
// pkg/redis.Client 의 메서드 집합과 일치하며 테스트에서 mock 으로 교체됩니다.
type redisIngestionLocker interface {
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
}

// NewRedisIngestionLock 은 RedisIngestionLock 을 생성합니다.
// ttl 이 0 이하이면 DefaultIngestionLockTTL 사용.
func NewRedisIngestionLock(locker redisIngestionLocker, ttl time.Duration) *RedisIngestionLock {
	if ttl <= 0 {
		ttl = DefaultIngestionLockTTL
	}
	return &RedisIngestionLock{locker: locker, ttl: ttl}
}

// Acquire 는 URL 의 진입 marker 를 SET NX EX 로 atomic 시도합니다.
//
// nil receiver / nil locker 보호: zero-value RedisIngestionLock 또는 NewRedisIngestionLock(nil, ...)
// 호출 시에도 panic 없이 에러 반환 — 호출자가 fail-open / 알림 등 처리 가능.
func (l *RedisIngestionLock) Acquire(ctx context.Context, url string) (bool, error) {
	if l == nil || l.locker == nil {
		return false, errors.New("ingestion lock locker is nil")
	}
	return l.locker.AcquireLock(ctx, ingestionKey(url), l.ttl)
}

// Invalidate 는 URL marker 를 즉시 제거합니다.
//
// nil receiver / nil locker 보호 — Acquire 와 동일 정책.
func (l *RedisIngestionLock) Invalidate(ctx context.Context, url string) error {
	if l == nil || l.locker == nil {
		return errors.New("ingestion lock locker is nil")
	}
	return l.locker.ReleaseLock(ctx, ingestionKey(url))
}

// ingestionKey 는 URL 을 sha256 해싱하여 Redis 키로 변환합니다.
//
// 호출자는 url 을 정규화 후 전달해야 — sha256 은 정확한 byte 매칭이라
// "https://example.com" 과 "https://example.com/" 이 다른 키가 됨.
func ingestionKey(url string) string {
	h := sha256.Sum256([]byte(url))
	return fmt.Sprintf("%s%s", ingestionKeyPrefix, hex.EncodeToString(h[:]))
}

// NoopIngestionLock 은 lock 을 사용하지 않는 no-op 구현체입니다.
// Redis 부재 환경 (단일 인스턴스, 테스트) 에서 fallback 으로 사용 — 항상 acquired=true.
//
// **운영 영향**: Noop 사용 시 dedup 비활성 — 같은 URL 이 여러 번 publish/fetch 가능.
// 단일 인스턴스에선 다른 worker 가 없으므로 race 없지만, 재배달 / 재시작 시 중복 가능.
type NoopIngestionLock struct{}

// Acquire always returns acquired=true (Noop).
func (NoopIngestionLock) Acquire(_ context.Context, _ string) (bool, error) { return true, nil }

// Invalidate is a no-op.
func (NoopIngestionLock) Invalidate(_ context.Context, _ string) error { return nil }

// 컴파일 타임 인터페이스 만족 검증.
var (
	_ IngestionLock = (*RedisIngestionLock)(nil)
	_ IngestionLock = NoopIngestionLock{}
)
