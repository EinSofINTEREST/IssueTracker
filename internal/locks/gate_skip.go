package locks

import "errors"

// GateSkipRetryDelay 는 StageGate 선점으로 건너뛴 메시지를 다시 시도하기까지의 지연입니다
// (이슈 #540).
//
// ProcessingLock TTL 의 절반으로 잡습니다.
//   - 락을 쥔 워커가 정상 처리 중이면 그 사이 끝날 가능성이 높고,
//   - crash / release 실패로 남은 stale 락이면 TTL(10분) 만료 뒤 재시도가 성공합니다.
//
// 더 짧게 잡으면 살아있는 홀더와 경쟁하며 재큐가 반복되고, 더 길게 잡으면 stale 락 복구가
// 늦어집니다.
const GateSkipRetryDelay = DefaultProcessingLockTTL / 2

// ErrStageGateHeld 는 StageGate 가 다른 워커에 점유되어 처리를 건너뛴 사유입니다.
//
// RetryScheduler 에 last_err 로 전달되어, 운영자가 재큐 사유를 실패(에러)와 구분할 수 있습니다.
var ErrStageGateHeld = errors.New("locks: stage gate held by another worker")
