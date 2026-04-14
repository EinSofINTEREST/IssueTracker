package worker

import (
	"context"
	"fmt"
	"time"
)

const (
	// jobLockKeyPrefix는 job 중복 처리 방지 락 키의 접두사입니다.
	jobLockKeyPrefix = "lock:job:"
	// DefaultJobLockTTL은 job 락의 기본 TTL입니다.
	// job 처리 최대 시간보다 충분히 길게 설정하여 정상 처리 중 만료를 방지합니다.
	DefaultJobLockTTL = 10 * time.Minute
)

// JobLocker는 job 중복 처리를 방지하는 분산 락 인터페이스입니다.
// 구현체는 goroutine-safe해야 합니다.
type JobLocker interface {
	// Acquire는 jobID에 대한 락을 획득합니다.
	// 이미 다른 worker가 처리 중이면 false, nil을 반환합니다.
	// 오류 발생 시 false와 error를 반환합니다.
	Acquire(ctx context.Context, jobID string) (bool, error)

	// Release는 jobID에 대한 락을 해제합니다.
	Release(ctx context.Context, jobID string) error
}

// RedisJobLocker는 Redis SET NX 기반 JobLocker 구현체입니다.
type RedisJobLocker struct {
	locker redisLocker
	ttl    time.Duration
}

// redisLocker는 Redis 락 조작을 추상화하는 내부 인터페이스입니다.
// pkg/redis.Client의 메서드 집합과 일치하며 테스트에서 mock으로 교체됩니다.
type redisLocker interface {
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
}

// NewRedisJobLocker는 RedisJobLocker를 생성합니다.
func NewRedisJobLocker(locker redisLocker, ttl time.Duration) *RedisJobLocker {
	return &RedisJobLocker{locker: locker, ttl: ttl}
}

func (l *RedisJobLocker) Acquire(ctx context.Context, jobID string) (bool, error) {
	return l.locker.AcquireLock(ctx, jobLockKey(jobID), l.ttl)
}

func (l *RedisJobLocker) Release(ctx context.Context, jobID string) error {
	return l.locker.ReleaseLock(ctx, jobLockKey(jobID))
}

func jobLockKey(jobID string) string {
	return fmt.Sprintf("%s%s", jobLockKeyPrefix, jobID)
}

// NoopJobLocker는 락을 사용하지 않는 no-op 구현체입니다.
// Redis가 없는 환경(단일 인스턴스, 테스트)에서 사용합니다.
type NoopJobLocker struct{}

func (NoopJobLocker) Acquire(_ context.Context, _ string) (bool, error) { return true, nil }
func (NoopJobLocker) Release(_ context.Context, _ string) error         { return nil }
