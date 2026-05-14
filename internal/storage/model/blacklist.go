// Package model 은 storage 계층의 도메인 데이터 타입 (Record / Filter / Enum) 만 정의합니다.
//
// 본 패키지는 인터페이스 / 의존성 / 동작을 가지지 않으며, 다른 모든 storage 하위 패키지가
// 본 패키지에 의존하지만 본 패키지는 아무것도 import 하지 않습니다 (time 외).
package model

import "time"

// BlacklistSource 는 블랙리스트 row 의 등록 출처입니다.
//
//   - BlacklistSourceManual : 운영자가 명시적으로 등록 (광고 / sponsored / redirect 등 도메인 지식 기반)
//   - BlacklistSourceAuto   : 시스템이 시그널 누적으로 자동 등록 (후속 이슈에서 도입)
//
// CHECK 제약으로 DB 레벨에서 두 값만 허용. 운영 가시성 + 자동 vs 수동 분리 정책 (예: auto 만
// 일괄 disable, manual 은 보존) 을 위해 컬럼화.
type BlacklistSource string

const (
	BlacklistSourceManual BlacklistSource = "manual"
	BlacklistSourceAuto   BlacklistSource = "auto"
)

// BlacklistMode 는 매칭된 URL 에 적용할 차단 정책입니다.
//
//   - BlacklistModeDrop             : URL 자체 drop — fetch / parse / 링크추출 모두 안 함 (default).
//     광고 / sponsored / redirect 처럼 그 안의 링크도 가치 없는 케이스.
//   - BlacklistModeExtractLinksOnly : list 로 강제 발행 — fetch + ParseLinks 만, ParsePage skip.
//     비-article 영역 (about / login / sitemap / menu) 처럼 그 안에
//     정상 article 링크가 있어 cascade 보존이 필요한 케이스.
//
// CHECK 제약으로 DB 레벨에서 두 값만 허용. 기존 row 는 default 'drop' (migration 017 후방 호환).
type BlacklistMode string

const (
	BlacklistModeDrop             BlacklistMode = "drop"
	BlacklistModeExtractLinksOnly BlacklistMode = "extract_links_only"
)

// BlacklistRecord 는 parser_blacklist 테이블의 단일 행입니다.
//
// page-parse 차단 정책 — 매칭 URL 은 article job 발행 단계에서 drop:
//   - HostPattern : URL host 매칭 (정확 일치, 호출자가 normalize)
//   - PathPattern : URL path 매칭 RE2 regex. "" 이면 host 전체 차단 (catch-all).
//     pattern 검증은 Insert 시 application 측 RE2 컴파일.
//   - Reason      : 운영 가시성 ("ad" / "redirect" / "sponsored" / ...)
//   - Source      : "manual" | "auto"
//   - Enabled     : 토글 — false 면 매칭 무시 (DELETE 대안, 임시 비활성)
type BlacklistRecord struct {
	ID          int64
	HostPattern string
	PathPattern string
	Reason      string
	Source      BlacklistSource
	Mode        BlacklistMode // 'drop' (default) | 'extract_links_only'
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// BlacklistFilter 는 List 조회 시 필터 조건입니다.
type BlacklistFilter struct {
	HostPattern string          // 빈 문자열이면 전체
	Source      BlacklistSource // 빈 문자열이면 전체
	OnlyEnabled bool            // true 면 enabled=true 만
	Limit       int             // 0 이면 기본값 (50)
	Offset      int
}
