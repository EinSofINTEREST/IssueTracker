// Package precheck 는 fetcher / parser / 기타 processor stage 가 URL 을 실제로 처리하기 전에
// 처리 가부를 일괄 판정하는 공용 진입 게이트입니다 (이슈 #425).
//
// 설계 목표:
//   - 다중 stage 의 진입점 (fetch / parse / outgoing chained URL filter 등) 에서 동일 게이트 호출
//   - cross-cutting 정책 (blacklist / 향후 rate_limit / robots / domain throttle 등) 의 plug-in
//   - 각 Source 는 의존성 역전 — 본 패키지는 BlacklistMatcher / rate_limit 등의 구현체를 모름
//
// 호출 패턴:
//
//	d := precheck.New(blacklistSource, ...)
//	v := d.CheckURL(ctx, jobURL)
//	switch v.Verdict {
//	case precheck.VerdictAllow:           // 정상 진행
//	case precheck.VerdictDrop:            // commit-only, 처리 skip
//	case precheck.VerdictExtractLinksOnly: // 파서 단계에서 list 강제 분기
//	}
package precheck

import (
	"context"
)

// Verdict 는 단일 URL 의 처리 결정입니다.
type Verdict int

const (
	// VerdictAllow: 정상 진행 (모든 Source 가 통과).
	VerdictAllow Verdict = iota
	// VerdictDrop: 처리 중단 — fetch / parse 스킵, commit-only.
	VerdictDrop
	// VerdictExtractLinksOnly: list 강제 분기 — fetch + ParseLinks 만 진행 (ParsePage skip).
	// blacklist 도메인의 'extract_links_only' mode 와 의미 동일.
	VerdictExtractLinksOnly
)

func (v Verdict) String() string {
	switch v {
	case VerdictAllow:
		return "allow"
	case VerdictDrop:
		return "drop"
	case VerdictExtractLinksOnly:
		return "extract_links_only"
	default:
		return "unknown"
	}
}

// Decision 은 Source 또는 Decider 의 판정 결과입니다.
//
//   - Verdict: 결정 자체
//   - Reason : 로깅 / 메트릭용 짧은 사유 (예: "blacklist:ad_redirect")
//   - Source : 결정을 내린 Source 이름 (예: "blacklist"). 다중 Source chain 의 가시성 확보.
type Decision struct {
	Verdict Verdict
	Reason  string
	Source  string
}

// Allowed 는 Verdict 가 Allow 인지 확인하는 helper 입니다.
func (d Decision) Allowed() bool { return d.Verdict == VerdictAllow }

// Source 는 단일 URL 의 처리 가부를 판정하는 plug-in 인터페이스입니다.
//
// 각 Source 는 자신의 도메인 정책 (blacklist 매칭 / rate_limit 제한 / robots 정책 등) 을 기반으로
// 판정합니다. Decider 가 등록된 Source 들을 순차 호출 — 첫 non-Allow 결정에서 short-circuit.
//
// 구현체는 goroutine-safe 해야 합니다.
type Source interface {
	// Name 은 Source 식별자입니다 (예: "blacklist"). Decision.Source 에 그대로 들어갑니다.
	Name() string

	// Check 는 단일 URL 에 대한 판정을 반환합니다.
	//
	// 본 Source 의 정책이 적용되지 않으면 VerdictAllow 반환 — 다음 Source 로 흐름이 계속됨.
	// 본 Source 가 Drop / ExtractLinksOnly 결정하면 그 자리에서 short-circuit.
	Check(ctx context.Context, rawURL string) Decision
}

// Decider 는 등록된 Source chain 으로 URL 처리 가부를 일괄 판정하는 boundary 입니다.
//
// 호출자는 본 인터페이스만 의존 — Source 구현체 / 추가는 wiring 단계에서 처리.
type Decider interface {
	// CheckURL 은 단일 URL 의 판정을 반환합니다.
	//
	// 등록된 Source 들을 등록 순서대로 순회 — 첫 non-Allow 결정에서 short-circuit.
	// 모든 Source 가 Allow 면 VerdictAllow 반환.
	CheckURL(ctx context.Context, rawURL string) Decision

	// CheckURLs 는 batch 편의 메서드 — 각 URL 에 대해 CheckURL 호출 후 슬라이스 반환.
	// 호출 순서대로 결과 슬라이스도 동일 순서. 빈 입력은 빈 슬라이스 반환.
	CheckURLs(ctx context.Context, urls []string) []Decision
}

// chainDecider 는 Source 슬라이스를 순차 호출하는 Decider 구현입니다.
type chainDecider struct {
	sources []Source
}

// New 는 Source 슬라이스를 chain 으로 묶은 Decider 를 반환합니다.
//
// sources 가 빈 슬라이스 / nil 이면 모든 URL 을 Allow 처리 (no-op gate).
func New(sources ...Source) Decider {
	// nil source 는 무시 — wiring 단계의 conditional 활성화 (예: BLACKLIST_ENABLED=false 시 nil 전달) 편의.
	filtered := make([]Source, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			filtered = append(filtered, s)
		}
	}
	return &chainDecider{sources: filtered}
}

func (d *chainDecider) CheckURL(ctx context.Context, rawURL string) Decision {
	for _, s := range d.sources {
		dec := s.Check(ctx, rawURL)
		if dec.Verdict != VerdictAllow {
			// short-circuit + Source 이름 보장 (구현체가 깜빡 비워둬도 추적 가능).
			if dec.Source == "" {
				dec.Source = s.Name()
			}
			return dec
		}
	}
	return Decision{Verdict: VerdictAllow}
}

func (d *chainDecider) CheckURLs(ctx context.Context, urls []string) []Decision {
	if len(urls) == 0 {
		return nil
	}
	out := make([]Decision, len(urls))
	for i, u := range urls {
		out[i] = d.CheckURL(ctx, u)
	}
	return out
}
