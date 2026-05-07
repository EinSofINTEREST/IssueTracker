package storage

import (
	"context"
	"time"
)

// BlacklistSource 는 블랙리스트 row 의 등록 출처입니다 (이슈 #295).
//
//   - BlacklistSourceManual : 운영자가 명시적으로 등록 (광고 / sponsored / redirect 등 도메인 지식 기반)
//   - BlacklistSourceAuto   : 시스템이 시그널 누적으로 자동 등록 (후속 이슈 — 본 PR scope 외)
//
// CHECK 제약으로 DB 레벨에서 두 값만 허용. 운영 가시성 + 자동 vs 수동 분리 정책 (예: auto 만
// 일괄 disable, manual 은 보존) 을 위해 컬럼화.
type BlacklistSource string

const (
	BlacklistSourceManual BlacklistSource = "manual"
	BlacklistSourceAuto   BlacklistSource = "auto"
)

// BlacklistMode 는 매칭된 URL 에 적용할 차단 정책입니다 (이슈 #297).
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

// BlacklistRecord 는 parsing_blacklist 테이블의 단일 행입니다 (이슈 #295).
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
	Mode        BlacklistMode // 'drop' (default) | 'extract_links_only' (이슈 #297)
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

// BlacklistRepository 는 parsing_blacklist 테이블에 대한 데이터 접근 인터페이스입니다 (이슈 #295).
//
// goroutine-safe: 모든 구현은 동시 호출 안전해야 함.
type BlacklistRepository interface {
	// Insert 는 새 블랙리스트 row 를 저장합니다. 자연키 (host_pattern, path_pattern) 충돌 시
	// ErrDuplicate 반환. PathPattern 이 빈 문자열이 아니면 RE2 컴파일 검증 — 실패 시 ErrInvalid.
	// 성공 시 r.ID / CreatedAt / UpdatedAt 채워짐.
	Insert(ctx context.Context, r *BlacklistRecord) error

	// Update 는 ID 로 row 를 갱신합니다 (자연키 변경 불가). Enabled / Reason / Source 만 변경 가능.
	// 존재하지 않으면 ErrNotFound.
	Update(ctx context.Context, r *BlacklistRecord) error

	// Delete 는 ID 로 row 를 삭제합니다. 존재하지 않아도 nil 반환 (idempotent).
	Delete(ctx context.Context, id int64) error

	// GetByID 는 ID 로 row 를 조회합니다.
	GetByID(ctx context.Context, id int64) (*BlacklistRecord, error)

	// FindEnabledByHost 는 host_pattern 매칭 enabled=TRUE row 들을 반환합니다 (Matcher 핫패스).
	//
	// 정렬: LENGTH(path_pattern) DESC — 더 구체적인 path 가 먼저 평가되도록 (parsing_rules 의
	// FindActiveCandidates 와 동일 정책). path_pattern="" (catch-all) 은 가장 마지막.
	//
	// 매칭 없으면 빈 슬라이스 + nil error.
	FindEnabledByHost(ctx context.Context, host string) ([]*BlacklistRecord, error)

	// List 는 필터 조건에 맞는 row 들을 반환합니다 (운영 대시보드용).
	List(ctx context.Context, filter BlacklistFilter) ([]*BlacklistRecord, error)
}
