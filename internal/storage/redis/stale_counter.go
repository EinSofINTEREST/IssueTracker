package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/primitive"
	"issuetracker/pkg/logger"
)

// staleCounter 는 Redis sorted set 기반 sliding window StaleCounter 구현체입니다.
//
// FailureCounter 와 동일 알고리즘 (ZADD + ZREMRANGEBYSCORE + EXPIRE + ZCARD 단일 PIPELINE).
// keyspace 만 분리: "stale:relearn:<host>:<type>".
type staleCounter struct {
	client    *goredis.Client
	threshold int
	window    time.Duration
	keyPrefix string
	now       func() time.Time
	log       *logger.Logger
}

// NewStaleCounter 는 Redis 기반 StaleCounter 를 생성합니다.
//
// client nil / threshold<1 / window<=0 시 error.
// keyPrefix 가 빈 문자열이면 "stale:relearn" 사용.
func NewStaleCounter(client *goredis.Client, threshold int, window time.Duration, keyPrefix string, log *logger.Logger) (primitive.StaleCounter, error) {
	if client == nil {
		return nil, errors.New("redisstore: NewStaleCounter requires non-nil redis client")
	}
	if threshold < 1 {
		return nil, fmt.Errorf("redisstore: NewStaleCounter requires threshold >= 1, got %d", threshold)
	}
	if window <= 0 {
		return nil, fmt.Errorf("redisstore: NewStaleCounter requires positive window, got %s", window)
	}
	if keyPrefix == "" {
		keyPrefix = "stale:relearn"
	}
	return &staleCounter{
		client:    client,
		threshold: threshold,
		window:    window,
		keyPrefix: keyPrefix,
		log:       log,
	}, nil
}

func (r *staleCounter) keyFor(host string, t model.TargetType) string {
	return r.keyPrefix + ":" + host + ":" + string(t)
}

func (r *staleCounter) Record(ctx context.Context, host string, t model.TargetType) (int, bool, error) {
	if host == "" {
		return 0, false, nil
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	key := r.keyFor(host, t)
	cutoff := now.Add(-r.window)

	// member 는 unique 보장용 — ns + 8 bytes random hex.
	// rand.Read 실패 시 nonce 가 빈 문자열이 되면 동일 ns 의 동시 record 가 같은 member 가 되어
	// ZADD 한 쪽이 무시됨 → 카운트 누락. 명시적으로 error 전파.
	nonce, err := randStaleNonce()
	if err != nil {
		return 0, false, fmt.Errorf("stale counter nonce for (%s, %s): %w", host, t, err)
	}
	member := strconv.FormatInt(now.UnixNano(), 10) + ":" + nonce
	score := float64(now.UnixNano())

	pipe := r.client.Pipeline()
	pipe.ZAdd(ctx, key, goredis.Z{Score: score, Member: member})
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff.UnixNano(), 10))
	pipe.Expire(ctx, key, 2*r.window)
	zcard := pipe.ZCard(ctx, key)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, false, fmt.Errorf("stale counter pipeline for (%s, %s): %w", host, t, err)
	}

	count := int(zcard.Val())
	reached := count >= r.threshold

	if r.log != nil {
		fields := map[string]interface{}{
			"host":        host,
			"target_type": string(t),
			"count":       count,
			"threshold":   r.threshold,
			"window":      r.window.String(),
		}
		if reached {
			r.log.WithFields(fields).Info("stale rule threshold reached — LLM relearn trigger eligible")
		} else {
			r.log.WithFields(fields).Debug("stale rule failure recorded")
		}
	}

	return count, reached, nil
}

// randStaleNonce 는 ZADD member unique 변별자용 random hex 를 생성합니다.
// rand.Read 실패 시 error 반환 — 호출자가 카운팅 자체를 포기 (빈 nonce fallback 은 동시 record
// 시 member 충돌을 유발하므로 회피).
func randStaleNonce() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("randStaleNonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}
