package validate

// reparse.go — validate 실패 시 parser 재학습 트리거 정책 (이슈 #364).
//
// validate 실패 사유 중 selector 보강으로 해결 가능한 케이스만 재학습 대상으로 분류:
//   - VAL_001 (PublishedAt required) — parsing_rule 에 published_at selector 부재 시
//   - VAL_003 (Title/Body min_length, min_word_count) — selector 가 잘못된 element 추출
//
// 다음 케이스는 재학습 대상 아님 — selector 변경으로 해결 불가:
//   - VAL_002 (format) / VAL_004 (max_length) / VAL_005 (quality) / VAL_006 (spam)
//
// claudegen 의 multi-turn agent 는 reason 컨텍스트 받고 validity=blacklist 판정 가능
// (예: PublishedAt 이 영원히 없는 메타/index 페이지). 본 모듈은 단순 코드 분류만 수행.

import (
	"errors"
	"strconv"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/queue"
)

// reparseEligibleCodes 는 selector 재학습으로 해결 가능한 validation 에러 코드 집합입니다.
var reparseEligibleCodes = map[string]struct{}{
	core.CodeValMissingField: {}, // VAL_001 — 필수 필드 누락 (PublishedAt 등)
	core.CodeValContentShort: {}, // VAL_003 — 텍스트 필드 최소 길이 미달 (Title/Body)
}

// readReparseCount 는 msg 의 validate_reparse_count 헤더를 정수로 파싱합니다.
//
// 헤더 부재 / 파싱 실패 시 0 (첫 시도) 반환 — 첫 진입이면 첫 reparse cycle 이 1 이 되도록.
func readReparseCount(msg *queue.Message) int {
	raw, ok := msg.Headers[core.HeaderValidateReparseCount]
	if !ok || raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// IsReparseEligible 은 err 가 selector 재학습 (parser → LLM 학습) 트리거 대상인지 판정합니다.
//
// 분류 기준 (이슈 #364):
//   - core.CrawlerError 가 아니면 false
//   - Code 가 reparseEligibleCodes 에 포함된 경우만 true
//
// 호출자는 본 true 결과 + reparse_count < MaxValidateReparseCount 일 때 새 CrawlJob 발행.
func IsReparseEligible(err error) bool {
	var cerr *core.CrawlerError
	if !errors.As(err, &cerr) {
		return false
	}
	_, ok := reparseEligibleCodes[cerr.Code]
	return ok
}
