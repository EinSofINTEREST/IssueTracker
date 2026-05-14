package precheck

import (
	"context"

	"issuetracker/internal/processor/parser/rule"
)

// blacklistSource 는 parser_blacklist 매칭을 precheck Source 로 노출합니다 (이슈 #425).
//
// rule.BlacklistMatcher 의 Classify 결과를 단일 URL 기준 Decision 으로 변환:
//
//   - Classify.Allowed         (매칭 X)  → VerdictAllow
//   - Classify.ExtractLinksOnly (mode=extract_links_only) → VerdictExtractLinksOnly
//   - 둘 다 미포함 (mode=drop)            → VerdictDrop
//
// best-effort: lookup 에러 등으로 인한 internal 실패 시 Allow (fail-open) — Matcher 의 기존 정책 일관.
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
	decision := s.matcher.Classify(ctx, []string{rawURL})
	if len(decision.Allowed) == 1 {
		return Decision{Verdict: VerdictAllow, Source: "blacklist"}
	}
	if len(decision.ExtractLinksOnly) == 1 {
		return Decision{
			Verdict: VerdictExtractLinksOnly,
			Source:  "blacklist",
			Reason:  "blacklist:extract_links_only",
		}
	}
	// 둘 다 미포함 → drop 매칭
	return Decision{
		Verdict: VerdictDrop,
		Source:  "blacklist",
		Reason:  "blacklist:drop",
	}
}
