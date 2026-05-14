package precheck

import (
	"context"

	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/storage/model"
)

// blacklistSource 는 parser_blacklist 매칭을 precheck Source 로 노출합니다 (이슈 #425).
//
// rule.BlacklistMatcher.MatchedMode (single-URL hot path) 로 mode 를 받아 Decision 으로 변환:
//
//   - 매칭 없음 (또는 lookup 에러)     → VerdictAllow (fail-open, Matcher 기존 정책 일관)
//   - mode='extract_links_only'        → VerdictExtractLinksOnly
//   - mode='drop'                      → VerdictDrop
//
// 이전 구현은 Classify(슬라이스) 를 1-URL 로 호출하여 매 호출마다 두 슬라이스 alloc — fetcher/
// parser 의 hot path 라 부담. MatchedMode 직접 호출로 alloc 0 (gemini medium 피드백, PR #436).
type blacklistSource struct {
	matcher *rule.BlacklistMatcher
}

// NewBlacklistSource 는 BlacklistMatcher 를 wrap 한 precheck Source 를 반환합니다.
//
// matcher 가 nil 이면 nil 반환 — precheck.New 가 nil source 를 자동 필터링하므로 wiring 측 분기 불필요
// (BLACKLIST_ENABLED=false 시 nil 전달 패턴).
func NewBlacklistSource(matcher *rule.BlacklistMatcher) Source {
	if matcher == nil {
		return nil
	}
	return &blacklistSource{matcher: matcher}
}

func (s *blacklistSource) Name() string { return "blacklist" }

func (s *blacklistSource) Check(ctx context.Context, rawURL string) Decision {
	mode, err := s.matcher.MatchedMode(ctx, rawURL)
	if err != nil {
		// lookup 에러 → Allow (fail-open). Matcher.Classify / Filter 와 동일 정책.
		return Decision{Verdict: VerdictAllow, Source: "blacklist"}
	}
	return modeToDecision(mode)
}

// CheckBatch 는 BatchSource 의 구현 — Classify (slice-returning batch) 결과를 Decision 슬라이스로 변환.
//
// urls 의 모든 entry 가 동일 인덱스로 정확히 매핑 — Classify 의 Allowed/ExtractLinksOnly 슬라이스
// 에서 두 번 lookup 보다 set membership 검사로 단순화.
func (s *blacklistSource) CheckBatch(ctx context.Context, urls []string) []Decision {
	if len(urls) == 0 {
		return nil
	}
	out := make([]Decision, len(urls))
	for i, u := range urls {
		mode, err := s.matcher.MatchedMode(ctx, u)
		if err != nil {
			out[i] = Decision{Verdict: VerdictAllow, Source: "blacklist"}
			continue
		}
		out[i] = modeToDecision(mode)
	}
	return out
}

func modeToDecision(mode model.BlacklistMode) Decision {
	switch mode {
	case model.BlacklistModeExtractLinksOnly:
		return Decision{Verdict: VerdictExtractLinksOnly, Source: "blacklist", Reason: "blacklist:extract_links_only"}
	case model.BlacklistModeDrop:
		return Decision{Verdict: VerdictDrop, Source: "blacklist", Reason: "blacklist:drop"}
	default:
		return Decision{Verdict: VerdictAllow, Source: "blacklist"}
	}
}
