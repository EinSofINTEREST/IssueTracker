package llmgen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// StaleCounter 는 stale rule 발생을 (host, target_type) 단위 sliding window 로 카운팅합니다 (이슈 #282).
//
// 기존 fetcher 의 FailureCounter (chromedp 업그레이드용) 와 별개의 keyspace / 임계값 보유:
//   - 임계값: STALE_RELEARN_THRESHOLD (default 10) — chromedp 업그레이드보다 높은 임계
//     (chromedp 가 먼저 시도되고, 그래도 fail 지속 시 LLM 재학습)
//   - 윈도우: STALE_RELEARN_WINDOW (default 2h) — 더 긴 관찰 기간
//
// thresholdReached=true 시 호출자 (parser_worker) 가 Generator.EnqueueStale 호출.
// goroutine-safe 필수.
type StaleCounter interface {
	// Record 는 (host, target_type) 의 stale parse failure 1건을 누적합니다.
	// 반환: (count 누적값, thresholdReached 임계 도달 여부, err 카운팅 실패).
	Record(ctx context.Context, host string, targetType storage.TargetType) (count int, thresholdReached bool, err error)
}

// noopStaleCounter — 비활성 (STALE_RELEARN_ENABLED=false 또는 Redis 미연결) 시 사용.
type noopStaleCounter struct{}

// NewNoopStaleCounter 는 항상 (0, false, nil) 을 반환하는 StaleCounter 입니다.
func NewNoopStaleCounter() StaleCounter { return noopStaleCounter{} }

func (noopStaleCounter) Record(_ context.Context, _ string, _ storage.TargetType) (int, bool, error) {
	return 0, false, nil
}

// redisStaleCounter — Redis sorted set 기반 sliding window 구현체.
//
// FailureCounter 와 동일 알고리즘 — ZADD + ZREMRANGEBYSCORE + EXPIRE + ZCARD 의 단일 PIPELINE.
// 차이점: keyspace 분리 ("stale:relearn:<host>:<type>") + 별도 threshold/window.
type redisStaleCounter struct {
	client    *goredis.Client
	threshold int
	window    time.Duration
	keyPrefix string
	now       func() time.Time
	log       *logger.Logger
}

// NewRedisStaleCounter 는 Redis 기반 stale counter 를 생성합니다.
//
// client nil / threshold<1 / window<=0 시 error.
// keyPrefix 가 빈 문자열이면 "stale:relearn" 사용.
func NewRedisStaleCounter(client *goredis.Client, threshold int, window time.Duration, keyPrefix string, log *logger.Logger) (StaleCounter, error) {
	if client == nil {
		return nil, errors.New("llmgen: NewRedisStaleCounter requires non-nil redis client")
	}
	if threshold < 1 {
		return nil, fmt.Errorf("llmgen: NewRedisStaleCounter requires threshold >= 1, got %d", threshold)
	}
	if window <= 0 {
		return nil, fmt.Errorf("llmgen: NewRedisStaleCounter requires positive window, got %s", window)
	}
	if keyPrefix == "" {
		keyPrefix = "stale:relearn"
	}
	return &redisStaleCounter{
		client:    client,
		threshold: threshold,
		window:    window,
		keyPrefix: keyPrefix,
		log:       log,
	}, nil
}

func (r *redisStaleCounter) keyFor(host string, t storage.TargetType) string {
	return r.keyPrefix + ":" + host + ":" + string(t)
}

func (r *redisStaleCounter) Record(ctx context.Context, host string, t storage.TargetType) (int, bool, error) {
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
	// ZADD 한 쪽이 무시됨 → 카운트 누락. 명시적으로 error 전파 (PR #294 gemini 피드백).
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
// 시 member 충돌을 유발하므로 회피, PR #294 gemini 피드백).
func randStaleNonce() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("randStaleNonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}
