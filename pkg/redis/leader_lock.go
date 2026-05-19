package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// LeaderLockKeyPrefix 는 leader-election 분산 락 키의 공통 접두사입니다 (이슈 #512).
//
// 본 락은 lock.go 의 AcquireLock/ReleaseLock 과 달리 **token-based ownership** 을 사용 —
// 다중 인스턴스 환경에서 TTL 만료 후 다른 instance 가 재획득한 lock 을 잘못 삭제하는 race
// (lock.go 의 약점) 를 회피. storage/redis/inflight_locker.go 와 동일 Lua release 패턴.
const LeaderLockKeyPrefix = "lock:leader:"

// luaLeaderRelease 는 소유권 확인 후 삭제하는 Lua 스크립트입니다.
// GET 과 DEL 의 원자성 보장 — 다른 인스턴스가 재획득한 lock 을 삭제하지 않습니다.
const luaLeaderRelease = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end`

// LeaderLockKey 는 role name 에 대응하는 leader lock 키를 반환합니다.
// 예: LeaderLockKey("buffer_drainer") → "lock:leader:buffer_drainer"
func LeaderLockKey(role string) string {
	return LeaderLockKeyPrefix + role
}

// LeaderLocker 는 token-based 다중 인스턴스 분산 leader lock 입니다.
//
// 사용 흐름 (single role, multi-instance):
//
//	locker, _ := redis.NewLeaderLocker(client, "buffer_drainer", 31*time.Second)
//	defer locker.Stop() // graceful shutdown 시 보유 lock 자동 release
//
//	if acquired, _ := locker.TryAcquire(ctx); acquired {
//	    defer locker.Release(ctx)
//	    // leader-only work
//	}
//
// 동작:
//   - TryAcquire: SET key token NX PX ttl — 성공 시 token 저장 + true 반환
//   - Release: Lua 스크립트로 token 일치 시에만 DEL (소유권 확인)
//   - TTL 만료 후 다른 인스턴스가 재획득한 lock 을 본 인스턴스의 Release 가 잘못 삭제 회피
//
// 자체 thread-safety: 단일 LeaderLocker 인스턴스는 단일 role 에 대한 단일 token 보유 —
// 동일 인스턴스에서 TryAcquire 를 중복 호출하면 두 번째 호출은 false 반환 (이미 보유 중).
type LeaderLocker struct {
	rdb   *goredis.Client
	role  string
	ttl   time.Duration
	mu    sync.Mutex
	token string // "" 이면 미보유, non-empty 면 보유 중
}

// NewLeaderLocker 는 token-based leader locker 를 생성합니다.
//
// rdb nil / role 빈 문자열 / ttl <= 0 → error.
// ttl 권장: tick interval × 1.1 (drain cycle 1회 안전 커버 + 약간 여유) — 너무 길면 leader
// crash 후 다른 instance 가 leader 가 되기까지 stall 길어짐.
func NewLeaderLocker(client *goredis.Client, role string, ttl time.Duration) (*LeaderLocker, error) {
	if client == nil {
		return nil, errors.New("leader locker: nil redis client")
	}
	if role == "" {
		return nil, errors.New("leader locker: empty role")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("leader locker: ttl must be positive, got %s", ttl)
	}
	return &LeaderLocker{
		rdb:  client,
		role: role,
		ttl:  ttl,
	}, nil
}

// TryAcquire 는 leader lock 획득을 시도합니다.
//
// 반환:
//   - (true, nil)  — 신규 leader 획득 성공. 호출자는 leader-only 작업 후 Release 호출 의무.
//   - (false, nil) — 다른 인스턴스가 이미 leader. 호출자는 작업 skip.
//   - (false, err) — Redis 인프라 에러. 호출자는 fail-closed 권장 (drain skip).
//
// 동일 LeaderLocker 인스턴스가 이미 lock 보유 중이면 (false, nil) — 이중 획득 방지.
// Release 호출 없이 lock 보유 중인 상태에서 TryAcquire 를 호출하는 것은 사용 오류.
func (l *LeaderLocker) TryAcquire(ctx context.Context) (bool, error) {
	l.mu.Lock()
	if l.token != "" {
		l.mu.Unlock()
		return false, nil // 이미 본 인스턴스가 보유 중 — 이중 획득 방지
	}
	l.mu.Unlock()

	token := newLeaderToken()
	key := LeaderLockKey(l.role)

	err := l.rdb.SetArgs(ctx, key, token, goredis.SetArgs{
		Mode: "NX",
		TTL:  l.ttl,
	}).Err()
	if errors.Is(err, goredis.Nil) {
		return false, nil // 다른 leader 가 보유 중
	}
	if err != nil {
		return false, fmt.Errorf("acquire leader lock %s: %w", key, err)
	}

	l.mu.Lock()
	l.token = token
	l.mu.Unlock()
	return true, nil
}

// Release 는 보유한 leader lock 을 안전하게 해제합니다 (token ownership 확인 후 DEL).
//
// 본 인스턴스가 lock 을 보유하지 않은 경우 (TryAcquire 가 false 반환 후 / 이미 Release 후 / Stop
// 후) noop — error 반환 안 함.
//
// Redis 인프라 에러는 warn 정도의 의미 — TTL 만료로 자연 회수되므로 error 만 반환하고 호출자가
// 로깅 결정.
func (l *LeaderLocker) Release(ctx context.Context) error {
	l.mu.Lock()
	token := l.token
	l.token = ""
	l.mu.Unlock()

	if token == "" {
		return nil // 미보유 — noop
	}

	key := LeaderLockKey(l.role)
	if err := l.rdb.Eval(ctx, luaLeaderRelease, []string{key}, token).Err(); err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("release leader lock %s: %w", key, err)
	}
	return nil
}

// HasLock 은 현재 본 인스턴스가 leader lock 을 보유하고 있는지 in-process 상태로 반환합니다
// (모니터링 / 디버깅용). Redis 까지 round-trip 하지 않음 — TTL 만료된 lock 은 여전히 true 일 수 있음.
func (l *LeaderLocker) HasLock() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.token != ""
}

// newLeaderToken 는 crypto/rand 기반의 고유 token (32자 hex) 을 생성합니다.
//
// 실패는 매우 드물지만 발생 시 빈 문자열 반환 — TryAcquire 가 빈 token 으로 SET NX → 다른
// 인스턴스의 빈 token lock 과 충돌 가능. 따라서 호출자는 빈 token 을 무시하지 않도록 명시적 panic.
func newLeaderToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("redis: crypto/rand failure generating leader token: %w", err))
	}
	return hex.EncodeToString(b)
}
