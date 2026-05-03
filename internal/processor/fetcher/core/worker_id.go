package core

import "context"

// 이슈 #229 — KafkaConsumerPool 의 worker goroutine 별 인덱스를 ctx 로 전달하기 위한 helper.
// ChromedpJobHandler 가 per-worker Semaphore 슬롯을 lookup 하는 데 사용. 이슈 #230 에서
// general.ChainHandler 의 chromedpChains slice lookup 에도 동일 키 재사용 — 이를 위해
// worker 패키지 외부 (사이클 없는 core) 에 위치.
//
// 컨텍스트 키 노출 정책: unexported type + sentinel — context.WithValue 충돌 회피 표준 패턴.
// 외부 패키지는 WithWorkerID / WorkerIDFromContext 만 사용.

type workerIDKey struct{}

// NoWorkerID 는 WorkerIDFromContext 가 worker_id 미설정을 나타낼 때 반환하는 값입니다.
// pool 외 호출 경로 (테스트, 다른 worker 종류) 에서 안전하게 fallback 분기할 수 있도록
// 음수 sentinel 을 사용합니다.
const NoWorkerID = -1

// WithWorkerID 는 ctx 에 worker_id (0..N-1) 를 첨부하여 반환합니다.
// KafkaConsumerPool 의 worker goroutine 이 처리 진입 시 1회 호출.
func WithWorkerID(ctx context.Context, workerID int) context.Context {
	return context.WithValue(ctx, workerIDKey{}, workerID)
}

// WorkerIDFromContext 는 ctx 에서 worker_id 를 추출합니다.
// 미설정 / 타입 불일치 시 NoWorkerID (-1) 반환 — 호출자는 fallback 분기 책임.
func WorkerIDFromContext(ctx context.Context) int {
	if ctx == nil {
		return NoWorkerID
	}
	v := ctx.Value(workerIDKey{})
	id, ok := v.(int)
	if !ok {
		return NoWorkerID
	}
	return id
}
