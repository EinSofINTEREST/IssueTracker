package rule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

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

// FailureReason 은 카운터에 INCR 되는 실패의 분류입니다 (이슈 #220).
//
// 카운터 자체는 reason 별 분리 없이 host 단위로 누적 — reason 은 audit log / metric 차원.
type FailureReason string

const (
	// FailureReasonRuleParseFailure: rule.Error 의 parse_failure / empty_selector 류 (selector 매칭 0건 / required selector 부재).
	FailureReasonRuleParseFailure FailureReason = "rule_parse_failure"
	// FailureReasonRuleNoRule: rule.Error 의 no_rule (host 에 active rule 없음).
	// 본 sub 의 카운팅 대상에서 제외 권장 — 이 경우는 LLM 자동 rule 생성 (이슈 #149) 의 책임 영역.
	// 그러나 운영자 분석을 위해 호출자가 선택 가능하도록 reason 정의는 유지.
	FailureReasonRuleNoRule FailureReason = "rule_no_rule"
	// FailureReasonEmptyBody: parse 자체는 성공했지만 Title / MainContent 텍스트 길이가 임계값 미달.
	// FetcherAutoUpgradeConfig.EmptyBodyTitleMin / EmptyBodyContentMin 으로 임계값 운영.
	FailureReasonEmptyBody FailureReason = "empty_body"
)

// FailureCounter 는 host 단위 fetcher 실패를 sliding window 로 카운팅합니다 (이슈 #220).
//
// 단계 3 (#221) 의 chromedp 자동 전환 트리거가 ThresholdReached=true 신호를 받아 fetcher_rules
// UPSERT + 실패 raw republish 를 발동합니다. 본 sub 는 카운팅 + 임계값 도달 신호까지만.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — parser_worker 가 동시에 Record 를 호출.
type FailureCounter interface {
	// Record 는 host 의 실패 1건을 누적하고 현재 window 내 카운트 + 임계값 도달 여부를 반환합니다.
	// reason 은 audit / metric 용 (구현체가 카운터에 분리 저장해도, 분리 안 해도 무방).
	// 카운팅 자체가 실패 (Redis 장애 등) 하면 error 반환 — 호출자가 graceful 분기.
	Record(ctx context.Context, host string, reason FailureReason) (count int, thresholdReached bool, err error)
}

// noopFailureCounter 는 카운팅 비활성 (FETCHER_AUTO_UPGRADE_ENABLED=false 또는 Redis 미연결) 시 사용합니다.
type noopFailureCounter struct{}

// NewNoopFailureCounter 는 항상 (0, false, nil) 을 반환하는 FailureCounter 를 반환합니다.
func NewNoopFailureCounter() FailureCounter { return noopFailureCounter{} }

func (noopFailureCounter) Record(ctx context.Context, host string, reason FailureReason) (int, bool, error) {
	return 0, false, nil
}

// redisFailureCounter 는 Redis sorted set 기반 sliding window FailureCounter 입니다.
//
// Sliding window 알고리즘:
//
//  1. ZADD score=now(unix-nano), member=unique-nonce  →  새 실패 timestamp 등록
//  2. ZREMRANGEBYSCORE 0 (now - window)              →  window 시작 이전 element 제거
//  3. EXPIRE key window+buffer                       →  key 자체 TTL (장기 미접근 시 자연 정리)
//  4. ZCARD                                          →  현재 window 내 카운트
//
// 위 4개 명령을 단일 PIPELINE 으로 atomic 하게 실행 — race 회피 + RTT 단일 round-trip.
type redisFailureCounter struct {
	client    *goredis.Client
	threshold int
	window    time.Duration
	keyPrefix string
	now       func() time.Time // 테스트 주입용 — 일반 사용 시 nil → time.Now
	log       *logger.Logger
}

// NewRedisFailureCounter 는 Redis 기반 sliding window FailureCounter 를 생성합니다.
//
// client 가 nil 이거나 threshold/window 가 비정상이면 error 반환 (이슈 #208 정책).
// keyPrefix 는 멀티 환경 분리용 ("dev", "prod" 등). 빈 문자열이면 "fetcher:fail" 사용.
func NewRedisFailureCounter(client *goredis.Client, threshold int, window time.Duration, keyPrefix string, log *logger.Logger) (FailureCounter, error) {
	if client == nil {
		return nil, errors.New("rule: NewRedisFailureCounter requires non-nil redis client")
	}
	if threshold < 1 {
		return nil, fmt.Errorf("rule: NewRedisFailureCounter requires threshold >= 1, got %d", threshold)
	}
	if window <= 0 {
		return nil, fmt.Errorf("rule: NewRedisFailureCounter requires positive window, got %s", window)
	}
	if keyPrefix == "" {
		keyPrefix = "fetcher:fail"
	}
	return &redisFailureCounter{
		client:    client,
		threshold: threshold,
		window:    window,
		keyPrefix: keyPrefix,
		log:       log,
	}, nil
}

// keyFor 는 host 의 카운터 sorted-set key 를 만듭니다.
func (r *redisFailureCounter) keyFor(host string) string {
	return r.keyPrefix + ":" + host
}

// Record 는 sliding window 알고리즘에 따라 실패 1건을 누적합니다.
func (r *redisFailureCounter) Record(ctx context.Context, host string, reason FailureReason) (int, bool, error) {
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
	// 0 으로 낮춘다 (gemini 피드백).
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
