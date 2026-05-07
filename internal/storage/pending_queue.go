package storage

import "context"

// PendingQueue 는 (host, targetType) scope 단위로 raw byte payload 를 큐잉하는 인터페이스입니다.
//
// 도메인 의존성 분리: storage 레이어는 payload 의 구조를 모르고 raw bytes 로만 취급합니다.
// 호출자 (예: llmgen.Generator) 가 marshal / unmarshal 책임을 보유 — storage 가 fetcher/core
// 같은 도메인 타입을 import 하지 않도록 layer 정합성 보장.
//
// 사용 시나리오: LLM 자동 룰 학습이 inflight 인 동안 같은 host 에 도착한 URL 들을 보존했다가
// 학습 완료 후 일괄 재투입.
type PendingQueue interface {
	// Push 는 (host, targetType) scope 의 큐 끝에 raw payload 를 적재합니다.
	// payload 는 호출자가 직접 marshal 한 bytes — storage 는 단순 보존.
	// 큐의 길이 상한 / TTL 등은 구현체별 정책 (예: redisstore.RedisPendingQueue).
	Push(ctx context.Context, host string, targetType TargetType, payload []byte) error

	// Flush 는 (host, targetType) scope 의 모든 payload 를 원자적으로 꺼내 반환합니다.
	// 반환 후 큐는 비어 있습니다. 손상된 payload 는 구현체가 skip 또는 그대로 반환 — 호출자가
	// unmarshal 시 추가 검증.
	Flush(ctx context.Context, host string, targetType TargetType) ([][]byte, error)
}
