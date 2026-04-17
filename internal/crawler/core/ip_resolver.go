package core

import "context"

// IPResolver는 URL에서 목적지 IP를 해석하는 인터페이스입니다.
// 구현체는 goroutine-safe해야 합니다.
//
// IPResolver resolves the destination IP address for a given URL.
type IPResolver interface {
	// Resolve는 URL의 호스트에 대해 DNS 해석을 수행하고 첫 번째 IP를 반환합니다.
	// context 취소/타임아웃 시 즉시 에러를 반환합니다.
	Resolve(ctx context.Context, rawURL string) (string, error)
}
