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

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// memberNonceLen 은 ZADD member 에 추가하는 random suffix 의 byte 길이입니다.
// 8 bytes hex (16 char) — 같은 nano + reason 조합이 동시 발생해도 충돌 확률 사실상 0.
// crypto/rand 실패 시 fallback 으로 빈 nonce — 그 경우 nano 정밀도에 의존하나 운영 영향 무시 가능.
const memberNonceLen = 8

// randNonce 는 ZADD member 의 unique 변별자로 사용할 random hex 문자열을 반환합니다.
// crypto/rand 실패는 매우 드물지만 발생 시 빈 문자열로 fallback (panic 회피).
func randNonce() string {
	b := make([]byte, memberNonceLen)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// failureCounter 는 Redis sorted set 기반 sliding window FailureCounter 구현체입니다.
//
// Sliding window 알고리즘:
//
//  1. ZADD score=now(unix-nano), member=unique-nonce  →  새 실패 timestamp 등록
//  2. ZREMRANGEBYSCORE 0 (now - window)              →  window 시작 이전 element 제거
//  3. EXPIRE key window+buffer                       →  key 자체 TTL (장기 미접근 시 자연 정리)
//  4. ZCARD                                          →  현재 window 내 카운트
//
// 위 4개 명령을 단일 PIPELINE 으로 atomic 하게 실행 — race 회피 + RTT 단일 round-trip.
type failureCounter struct {
	client    *goredis.Client
	threshold int
	window    time.Duration
	keyPrefix string
	now       func() time.Time // 테스트 주입용 — 일반 사용 시 nil → time.Now
	log       *logger.Logger
}

// NewFailureCounter 는 Redis 기반 sliding window FailureCounter 를 생성합니다.
//
// client 가 nil 이거나 threshold/window 가 비정상이면 error 반환.
// keyPrefix 는 멀티 환경 분리용 ("dev", "prod" 등). 빈 문자열이면 "fetcher:fail" 사용.
func NewFailureCounter(client *goredis.Client, threshold int, window time.Duration, keyPrefix string, log *logger.Logger) (storage.FailureCounter, error) {
	if client == nil {
		return nil, errors.New("redisstore: NewFailureCounter requires non-nil redis client")
	}
	if threshold < 1 {
		return nil, fmt.Errorf("redisstore: NewFailureCounter requires threshold >= 1, got %d", threshold)
	}
	if window <= 0 {
		return nil, fmt.Errorf("redisstore: NewFailureCounter requires positive window, got %s", window)
	}
	if keyPrefix == "" {
		keyPrefix = "fetcher:fail"
	}
	return &failureCounter{
		client:    client,
		threshold: threshold,
		window:    window,
		keyPrefix: keyPrefix,
		log:       log,
	}, nil
}

func (r *failureCounter) keyFor(host string) string {
	return r.keyPrefix + ":" + host
}

// Record 는 sliding window 알고리즘에 따라 실패 1건을 누적합니다.
func (r *failureCounter) Record(ctx context.Context, host string, reason storage.FailureReason) (int, bool, error) {
	if host == "" {
		return 0, false, nil
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	key := r.keyFor(host)
	cutoff := now.Add(-r.window)

	// member 는 timestamp + reason + random nonce — Redis sorted set 은 (score, member) 의
	// member 가 unique 해야 ZADD 가 새 entry 로 인정됨. nano 정밀도가 보장되지 않는 환경 (Windows
	// 일부 / 가상화 환경 etc.) 에서 같은 ns + reason 조합이 동시 발생하면 ZADD 가 중복 처리하여
	// 카운트가 누락될 수 있으므로 crypto/rand 의 8 bytes hex nonce 를 추가해 충돌 확률을 사실상
	// 0 으로 낮춘다.
	member := strconv.FormatInt(now.UnixNano(), 10) + ":" + string(reason) + ":" + randNonce()
	score := float64(now.UnixNano())

	// PIPELINE — atomic 단일 RTT.
	pipe := r.client.Pipeline()
	pipe.ZAdd(ctx, key, goredis.Z{Score: score, Member: member})
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff.UnixNano(), 10))
	// EXPIRE: window 의 2배 — 장기 미접근 host 의 key 자연 정리, sliding 정확도 보장.
	pipe.Expire(ctx, key, 2*r.window)
	zcard := pipe.ZCard(ctx, key)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, false, fmt.Errorf("redis failure counter pipeline for host %s: %w", host, err)
	}

	count := int(zcard.Val())
	threshold := count >= r.threshold

	if r.log != nil {
		fields := map[string]interface{}{
			"host":      host,
			"reason":    string(reason),
			"count":     count,
			"threshold": r.threshold,
			"window":    r.window.String(),
		}
		if threshold {
			r.log.WithFields(fields).Info("fetcher failure threshold reached — chromedp upgrade trigger eligible")
		} else {
			r.log.WithFields(fields).Debug("fetcher failure recorded")
		}
	}

	return count, threshold, nil
}
