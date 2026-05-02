package rule

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// MetadataKeyForceFetcherToken 은 force_fetcher metadata 의 provenance 검증용 보조 키 (이슈 #221).
//
// Upgrader 가 process-local secret token 을 함께 부착, ChainHandler 가 검증 후에만 force_fetcher
// 를 honor. 외부 publisher (현재는 없으나 미래 확장 시) 가 force_fetcher 임의 지정으로 chromedp
// 를 강제하지 못하도록 internal-only 보장 (CodeRabbit 피드백).
const MetadataKeyForceFetcherToken = "force_fetcher_token"

var (
	forceTokenMu sync.RWMutex
	forceToken   string
)

// InitForceFetcherToken 은 process startup 시 1회 호출하여 force_fetcher token 을 초기화합니다.
//
// crypto/rand 16 bytes hex (32 char). 외부 source 가 추측 불가. 호출 후 ForceFetcherTokenValue /
// ValidateForceFetcherToken 가 일관된 값으로 동작.
//
// 이미 초기화된 상태에서 재호출은 idempotent 하지 않음 — token 갱신 발생 (테스트용 외 운영 환경
// 에서는 1회만 호출).
func InitForceFetcherToken() error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("init force_fetcher token: %w", err)
	}
	forceTokenMu.Lock()
	forceToken = hex.EncodeToString(b)
	forceTokenMu.Unlock()
	return nil
}

// ForceFetcherTokenValue 는 현재 process token 을 반환합니다.
//
// Init 호출 전이면 빈 문자열 — Upgrader 가 metadata 에 빈 토큰 부착 → ChainHandler 의 검증
// 자연 실패 → force_fetcher 무력화 (안전한 fallback).
func ForceFetcherTokenValue() string {
	forceTokenMu.RLock()
	defer forceTokenMu.RUnlock()
	return forceToken
}

// ValidateForceFetcherToken 는 provided 값이 process token 과 일치하는지 검증합니다.
//
// Init 안 된 상태 (forceToken="") 에서는 항상 false — 검증 자연 실패로 force_fetcher 무력화.
// constant-time 비교는 token leakage 위협이 낮아 (process-local) 단순 == 사용.
func ValidateForceFetcherToken(provided string) bool {
	forceTokenMu.RLock()
	defer forceTokenMu.RUnlock()
	return forceToken != "" && provided == forceToken
}
