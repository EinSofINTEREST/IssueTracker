// Package parse 는 pkg/config 의 모든 sub-package 가 env 값을 일관된 정책으로 파싱/검증하는
// helper 모음입니다 (이슈 #439).
//
// 각 helper 는:
//   - env 변수가 비어있으면 noop (default 값 보존)
//   - 비어있지 않으면 파싱 시도, 실패 시 명확한 wrap 에러 반환
//   - 의미적 경계 검증 (port 범위 / 양수 / 비음수 / ratio 등) 실패 시 명확한 에러
//
// 잘못된 env 값에 의한 silent degraded 동작 (무한 대기 / 0 port 등) 방지가 목적.
//
// 호출 패턴 (Load 함수 내):
//
//	if err := parse.Port("POSTGRES_PORT", &cfg.Port); err != nil {
//	    return DBConfig{}, err
//	}
package parse

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Port 는 env 를 TCP/IP port (1-65535) 로 파싱합니다.
func Port(key string, dest *int) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("%s %d out of range (1-65535)", key, n)
	}
	*dest = n
	return nil
}

// PositiveDuration 은 env 를 time.Duration 으로 파싱 + 양수 (> 0) 검증.
//
// 사용: timeout 류 — 0 / 음수 timeout 은 무한 대기 / 즉시 실패 위험.
func PositiveDuration(key string, dest *time.Duration) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if d <= 0 {
		return fmt.Errorf("%s %s must be positive", key, d)
	}
	*dest = d
	return nil
}

// NonNegativeDuration 은 env 를 time.Duration 으로 파싱 + 비음수 (>= 0) 검증.
//
// 사용: 0 이 "비활성" 의미인 경우 (예: QueryTimeout=0 → timeout 미적용).
func NonNegativeDuration(key string, dest *time.Duration) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if d < 0 {
		return fmt.Errorf("%s %s must be non-negative", key, d)
	}
	*dest = d
	return nil
}

// PositiveInt 는 env 를 int 로 파싱 + 양수 검증 (> 0).
func PositiveInt(key string, dest *int) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if n <= 0 {
		return fmt.Errorf("%s %d must be positive", key, n)
	}
	*dest = n
	return nil
}

// NonNegativeInt 는 env 를 int 로 파싱 + 비음수 검증 (>= 0).
//
// 사용: 0 이 "비활성" 또는 "무제한" 의미인 경우 (예: MaxBacklog=0 → throttle 비활성).
func NonNegativeInt(key string, dest *int) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if n < 0 {
		return fmt.Errorf("%s %d must be non-negative", key, n)
	}
	*dest = n
	return nil
}

// PositiveInt32 는 PositiveInt 의 int32 변형.
func PositiveInt32(key string, dest *int32) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if n <= 0 {
		return fmt.Errorf("%s %d must be positive", key, n)
	}
	*dest = int32(n)
	return nil
}

// NonNegativeInt32 는 NonNegativeInt 의 int32 변형.
func NonNegativeInt32(key string, dest *int32) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if n < 0 {
		return fmt.Errorf("%s %d must be non-negative", key, n)
	}
	*dest = int32(n)
	return nil
}

// NonNegativeInt64 는 NonNegativeInt 의 int64 변형.
func NonNegativeInt64(key string, dest *int64) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if n < 0 {
		return fmt.Errorf("%s %d must be non-negative", key, n)
	}
	*dest = n
	return nil
}

// Ratio 는 env 를 float64 로 파싱 + [0.0, 1.0] 범위 검증.
//
// 사용: 비율 / 확률 류 (cap_ratio / sample_rate 등).
func Ratio(key string, dest *float64) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	if f < 0 || f > 1 {
		return fmt.Errorf("%s %g out of range [0.0, 1.0]", key, f)
	}
	*dest = f
	return nil
}

// Bool 은 env 를 bool 로 파싱합니다.
func Bool(key string, dest *bool) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", key, v, err)
	}
	*dest = b
	return nil
}

// String 은 env 를 string 으로 그대로 적용 (비어있지 않을 때만).
//
// 다른 helper 와 시그니처 일관성을 위해 error 를 반환 — 실제 실패 케이스는 없어 항상 nil.
// 호출부에서 `for _, op := range []error{...}` 패턴에 합류시킬 수 있도록 하기 위함.
func String(key string, dest *string) error {
	if v := os.Getenv(key); v != "" {
		*dest = v
	}
	return nil
}

// Enum 은 env 를 string 으로 파싱하되 allowed 집합에 포함되어야 함.
//
// 사용: provider / mode 류 (typo 로 인한 silent fallback 회피).
func Enum(key string, allowed []string, dest *string) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	for _, a := range allowed {
		if v == a {
			*dest = v
			return nil
		}
	}
	return fmt.Errorf("%s %q not in allowed set [%s]", key, v, strings.Join(allowed, ", "))
}
