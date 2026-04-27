package urlguard

import (
	"strings"
)

// PatternGuard 는 substring 패턴 기반의 Guard 구현입니다.
// 등록된 패턴 중 하나라도 URL 에 포함되면 차단합니다 (case-insensitive).
//
// goroutine-safe — 생성 후 패턴 집합은 불변입니다 (NewPatternGuard 시 복사).
//
// 매칭 정책 (substring):
//   - URL 을 소문자 변환 후 각 패턴(소문자 사전 변환됨)을 strings.Contains 로 검사
//   - 호스트·경로·쿼리 어디에 포함되어도 매칭 (예: "/rss" 패턴은 "rss.cnn.com" 호스트와
//     "/rss/foo.xml" path 양쪽에 매칭)
//   - 단순 substring 매칭이 의도치 않은 매칭을 일으킬 수 있으므로 패턴은 보수적으로 선정
type PatternGuard struct {
	// patternsLower 는 소문자로 사전 변환된 패턴 집합입니다.
	// Allow 호출마다 ToLower 를 반복하지 않기 위함.
	patternsLower []string
}

// NewPatternGuard 는 주어진 substring 패턴들로 새 PatternGuard 를 생성합니다.
// 패턴은 소문자로 사전 변환되어 저장되며, 호출자 슬라이스 변경으로부터 독립적인
// 복사본을 보유합니다.
func NewPatternGuard(patterns ...string) *PatternGuard {
	lower := make([]string, len(patterns))
	for i, p := range patterns {
		lower[i] = strings.ToLower(p)
	}
	return &PatternGuard{patternsLower: lower}
}

// Allow 는 url 이 등록된 어떤 패턴과도 매칭되지 않으면 (true, "") 를,
// 매칭되면 (false, "blocked by pattern: <pattern>") 을 반환합니다.
// 빈 url 은 허용합니다 (호출자가 별도 검증).
func (g *PatternGuard) Allow(url string) (bool, string) {
	if url == "" {
		return true, ""
	}
	lower := strings.ToLower(url)
	for _, pat := range g.patternsLower {
		if pat == "" {
			continue
		}
		if strings.Contains(lower, pat) {
			return false, "blocked by pattern: " + pat
		}
	}
	return true, ""
}

// defaultPatterns 는 도메인 디폴트 차단 패턴입니다 (unexported — 외부 변경 차단).
//
// 정책 근거:
//   - "/rss" : RSS 피드 URL 일괄 차단 (이슈 #119 의 CNN RSS 잔존 사고 대응).
//     pkg/links 의 defaultExcludePatterns 와 동일 패턴.
//   - "mailto:" / "tel:" : 비-HTTP 스킴은 외부 fetch 대상이 아님.
//   - "javascript:" : 클라이언트 사이드 코드 — fetch 의미 없음.
//
// 운영 환경에서 추가 패턴이 필요한 경우 NewPatternGuard 로 별도 인스턴스 구성.
var defaultPatterns = []string{
	"/rss",
	"mailto:",
	"tel:",
	"javascript:",
}

// DefaultPatterns 는 디폴트 차단 패턴 목록의 복사본을 반환합니다.
// 외부에서 수정해도 내부 상태와 다른 호출자의 결과에 영향을 주지 않습니다.
//
// 운영 진단/문서화 목적으로 패턴 목록을 조회하거나, 커스텀 PatternGuard 를
// 구성할 때 base 로 사용하기 위함:
//
//	patterns := append(urlguard.DefaultPatterns(), "/extra-block")
//	guard := urlguard.NewPatternGuard(patterns...)
func DefaultPatterns() []string {
	out := make([]string, len(defaultPatterns))
	copy(out, defaultPatterns)
	return out
}

// Default 는 디폴트 패턴으로 구성한 Guard 인스턴스를 반환합니다.
func Default() Guard {
	return NewPatternGuard(defaultPatterns...)
}
