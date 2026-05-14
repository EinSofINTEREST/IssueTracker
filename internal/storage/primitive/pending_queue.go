package primitive

import (
	"context"

	"issuetracker/internal/storage/model"
)

// PendingQueue 는 (host, targetType) scope 단위로 raw byte payload 를 큐잉하는 인터페이스입니다.
//
// 도메인 의존성 분리: storage 레이어는 payload 의 구조를 모르고 raw bytes 로만 취급합니다.
// 호출자 (예: llmgen.Generator) 가 marshal / unmarshal 책임을 보유.
//
// 사용 시나리오: LLM 자동 룰 학습이 inflight 인 동안 같은 host 에 도착한 URL 들을 보존했다가
// 학습 완료 후 일괄 재투입.
type PendingQueue interface {
	// Push 는 (host, targetType) scope 의 큐 끝에 raw payload 를 적재합니다.
	Push(ctx context.Context, host string, targetType model.TargetType, payload []byte) error

	// Flush 는 (host, targetType) scope 의 모든 payload 를 원자적으로 꺼내 반환합니다.
	Flush(ctx context.Context, host string, targetType model.TargetType) ([][]byte, error)
}
