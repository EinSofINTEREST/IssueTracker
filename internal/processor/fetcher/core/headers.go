package core

// Kafka 메시지 헤더 키 — stage 간 (fetcher → parser → validate) 시그널 전파에 사용.
//
// 헤더 채택 이유: payload (CrawlJob / RawContentRef / ContentRef) 스키마 변경 없이 메타데이터
// 만 전달. JSON 마이그레이션 비용 회피 + 헤더는 wire-level 에서 항상 접근 가능.
const (
	// HeaderTargetType: 처리 대상 타입 — "list" / "page" / "search".
	// fetcher 가 host 별 chain handler 분기 + parser 가 ParseLinks vs ParsePage 분기.
	HeaderTargetType = "target_type"

	// HeaderCrawler: source crawler 식별자 (예: "naver", "cnn"). 로깅 + 재큐 시 보존용.
	HeaderCrawler = "crawler"

	// HeaderTimeoutMs: fetch / parse 단계의 timeout (ms). scheduler 가 entry 별로 다른 값 부여 가능.
	HeaderTimeoutMs = "timeout_ms"

	// HeaderValidateReparseCount: validator → parser 재학습 cycle 회차 (이슈 #363/#364).
	// "0" 또는 미설정 = 일반 fetch / 첫 시도. "1".."MaxValidateReparseCount" = N번째 reparse.
	// validate worker 가 reject 시 본 count 를 +1 한 새 CrawlJob 을 publish.
	HeaderValidateReparseCount = "validate_reparse_count"

	// HeaderValidateReparseReason: validate worker 가 reject 한 사유 문자열 (이슈 #363/#365).
	// claudegen 의 LLM prompt 에 컨텍스트로 주입되어 multi-turn agent 가 selector 보강 또는
	// validity=blacklist 결정에 활용. 헤더 부재 = reparse 가 아닌 일반 처리 경로.
	HeaderValidateReparseReason = "validate_reparse_reason"
)

// MaxValidateReparseCount: validate → parser 재학습 cycle 최대 횟수 (이슈 #364).
//
// count 도달 후에도 validate 가 실패하면 영구 DLQ + content delete. 무한 루프 차단.
// 값 2 의 의미: 원본 fetch 1회 + 재학습 reparse 2회 = 총 fetch 3회 / URL.
const MaxValidateReparseCount = 2
