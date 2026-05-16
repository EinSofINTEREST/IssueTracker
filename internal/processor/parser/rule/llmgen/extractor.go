package llmgen

import (
	"context"
	"issuetracker/internal/storage/model"
)

// PageType 은 LLM 이 추출한 페이지의 도메인 분류입니다.
//
// 본 분류는 정보 신뢰도 시스템 (별도 후속 이슈) 의 1차 입력으로 사용됩니다 —
// news / paper 는 높은 신뢰도, commercial 은 낮은 신뢰도 weight 등으로 매핑.
//
// 새 분류는 추가될 수 있는 확장 지점 — 본 패키지는 string 으로 받아 storage 에 그대로 저장,
// 검증은 application 측 (운영자가 잘못된 분류를 후처리로 정정 가능).
type PageType string

const (
	PageTypeNews        PageType = "news"       // 보도/저널리즘
	PageTypeCommunity   PageType = "community"  // 게시판/포럼
	PageTypeInfo        PageType = "info"       // 위키/공식 가이드/문서
	PageTypeCommercial  PageType = "commercial" // 쇼핑/홍보
	PageTypePaper       PageType = "paper"      // 학술/논문
	PageTypeOther       PageType = "other"      // 기타
	PageTypeUnspecified PageType = ""           // 미분류 (legacy / Extract 경로)
)

// BlacklistDecision 은 LLM 이 페이지를 \"파싱에 부적합\" 으로 판단했을 때의 결정입니다.
//
// 호출자 (Generator) 가 본 결정을 받으면 셀렉터 INSERT 를 skip 하고 parser_blacklist 에
// source='auto' 로 등록 — 동일 host+path 가 다음 사이클에서 fetch / parse 되지 않음.
type BlacklistDecision struct {
	// Reason 은 운영자가 사후 분류 / 검증할 수 있도록 한국어로 작성된 사유.
	// parser_blacklist.reason 컬럼에 그대로 저장.
	Reason string

	// Mode 는 parser_blacklist 등록 시 적용할 차단 정책입니다 (이슈 #480).
	//   - "drop"               : URL 자체 drop — 광고 / interstitial / empty / captcha 등 본문/링크 모두 무가치한 케이스
	//   - "extract_links_only" : list 모드로 강등 — 카테고리 / 섹션 인덱스 등 본문은 없지만 article-like link 가 있는 케이스
	// 빈 문자열이면 호출자 (service.HandleLLMDecision) 가 default "drop" 으로 보정 — backward compat.
	Mode model.BlacklistMode
}

// ExtractResult 는 EnrichedExtractor 의 다단계 추출 결과입니다.
//
// Blacklist != nil 인 경우 Selectors / PageType 은 의미 없음 — 호출자는 Blacklist 분기로
// 즉시 넘어가야 함. Blacklist == nil 이면 Selectors 가 정상 추출 결과 + PageType 메타데이터.
type ExtractResult struct {
	// Selectors: target_type 별 추출된 CSS 셀렉터 맵. Blacklist != nil 이면 비어있을 수 있음.
	Selectors model.SelectorMap

	// PageType: 페이지 도메인 분류. 빈 문자열이면 미분류. Blacklist != nil 이면 의미 없음.
	PageType PageType

	// PageTypeConfidence: 0.0 ~ 1.0 — LLM 의 자기 보고 신뢰도. 운영자가 낮은 confidence 를
	// 사후 검토 trigger 로 사용 가능.
	PageTypeConfidence float64

	// Article: 페이지가 단일 뉴스 기사 본문 (article body) 인지 — LLM 자체 분류 (이슈 #423).
	// PageType 과 직교 (예: PageType=news + Article=true 가 "기사 본문", PageType=news + Article=false
	// 가 "뉴스 인덱스"). 다운스트림 validator (news 도메인) 가 Article=true 에만 PublishedAt 필수 강제.
	// Blacklist != nil 이면 의미 없음 (zero-value false).
	Article bool

	// ArticleConfidence: 0.0 ~ 1.0 — Article 분류에 대한 LLM 자기 보고 신뢰도.
	// 운영자 사후 검토 trigger / weight 입력으로 사용 가능.
	ArticleConfidence float64

	// Blacklist: 페이지가 파싱 부적합 (광고 / interstitial / 빈 페이지 등) 일 때 비-nil.
	// nil 이면 정상 추출 결과로 분기.
	Blacklist *BlacklistDecision
}

// EnrichedExtractor 는 SelectorExtractor 의 multi-step 확장 인터페이스입니다.
//
// SelectorExtractor 는 셀렉터만 반환하는 단일 분기였으나, 본 인터페이스는 다음 3가지 단계를
// 통합:
//  1. 페이지 유효성 판정 (광고 / 빈 페이지 등 → blacklist)
//  2. 페이지 타입 분류 (news / community / ...)
//  3. 셀렉터 추출 (기존 동작)
//
// claudegen 등 multi-turn agent 능력이 풍부한 backend 가 본 인터페이스를 구현. Gemini Flash
// 등 단순 provider 는 SelectorExtractor 만 구현 — Generator 가 인터페이스 type assertion 으로
// 자동 분기.
type EnrichedExtractor interface {
	ExtractEnriched(ctx context.Context, host string, targetType model.TargetType, html string) (*ExtractResult, error)
}
