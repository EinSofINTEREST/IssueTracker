package llmgen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"issuetracker/internal/storage/model"
	"issuetracker/pkg/logger"
)

// audit.go 는 LLM 룰 생성 시도의 audit 기록입니다 (이슈 #583).
//
// # 왜 테이블이 아니라 구조화 로그인가
//
// 이슈 #583 이 저장 위치를 미결로 남겼고, 구조화 로그(B안)로 결정됐다. 근거:
//
//   - 신규 테이블은 마이그레이션 + repository + **보존 정책(purge 주기)** 이 따라온다.
//     audit row 는 무한 증가하므로 purge 가 필수인데, 그 운영 부담이 audit 의 가치보다
//     먼저 확실하지 않다
//   - 로그는 이미 중앙 수집 대상이고 30일 보존 정책이 있다
//     (`.claude/rules/04-error-handling.md`)
//   - 나중에 실제 질의 요구가 생기면 그때 테이블로 올릴 수 있다. 반대 방향(테이블을
//     걷어내는 것)이 더 비싸다
//
// # 토큰 사용량을 기록하지 않는 이유
//
// `llm.Response.InputTokens` / `OutputTokens` 는 **HTTP provider (gemini / openai /
// anthropic) 만 채운다.** 룰 생성이 실제로 쓰는 claudegen / codex 경로는 docker exec +
// stdout 이라 토큰 정보가 **애초에 존재하지 않는다** (이슈 #539 조사, 이슈 #558 도 같은
// 제약으로 호출 수 비율을 택했다).
//
// 따라서 토큰 열을 두면 대부분 비어 있고, 비어 있음이 "0 토큰" 인지 "정보 없음" 인지
// 구별되지 않아 오히려 해석을 그르친다. 호출 수(metric)가 비용 대리 지표 역할을 한다.

// auditEvent 는 audit 로그의 msg 문자열입니다 — 로그 수집기에서 필터 기준이 됩니다.
const auditEvent = "llmgen rule generation attempt"

// selectorHashLen 은 로그에 남길 selector hash 의 hex 길이입니다.
//
// 전체 selector 를 로그에 실으면 한 줄이 수 KB 가 되고 수집 비용이 커진다. hash 는
// "같은 결과인지" 비교에 충분하며, 전체 내용은 `parser_rules.description` 에 남는다.
const selectorHashLen = 12

// selectorFingerprint 는 selector 집합의 짧은 지문을 만듭니다.
//
// 용도: 같은 host 를 반복 생성할 때 **결과가 실제로 달라졌는지** 를 로그만으로 판별.
// 같은 지문이 반복되면 재학습이 의미 없이 돌고 있다는 신호다.
//
// marshal 실패 시 빈 문자열 — audit 은 best-effort 이며, 지문 하나 때문에 생성 경로를
// 실패시키지 않는다.
func selectorFingerprint(sm model.SelectorMap) string {
	b, err := json.Marshal(sm)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:selectorHashLen]
}

// classifyOutcome 은 runOnce 의 반환 에러를 audit / metric 상태로 분류합니다.
//
// 반환하는 status 는 GenStatus* 상수 중 하나이거나, metric 대상이 아닌 경우 빈 문자열.
// blacklist 는 실패가 아니라 정상 완료(룰을 만들지 않기로 한 결정)라 별도 상태로 둔다 —
// 실패율에 섞이면 "LLM 이 자주 실패한다" 는 잘못된 인상을 준다.
func classifyOutcome(err error) (auditResult string, metricStatus string) {
	switch {
	case err == nil:
		return GenStatusSuccess, GenStatusSuccess
	case errors.Is(err, errCallBudgetExceeded):
		return GenStatusCapExceeded, GenStatusCapExceeded
	case errors.Is(err, errBlacklistedNoRule):
		// metric 상태 없음 — 실패도 성공도 아니다.
		return auditResultBlacklisted, ""
	default:
		var sve *selectorValidationError
		if errors.As(err, &sve) {
			return GenStatusValidationFail, GenStatusValidationFail
		}
		return GenStatusLLMError, GenStatusLLMError
	}
}

// auditResultBlacklisted 는 LLM 이 페이지를 blacklist 로 판정해 룰을 만들지 않은 경우입니다.
const auditResultBlacklisted = "blacklisted"

// auditRecord 는 한 번의 룰 생성 시도를 구조화 로그로 남깁니다 (이슈 #583).
//
// 필드는 `.claude/rules/04-error-handling.md` 의 snake_case 규약을 따르며, 기존 표준 키
// (`host` 는 컴포넌트 특화 키로 이미 llmgen 전반에서 사용) 와 일관됩니다.
//
// 레벨 선택: 결과와 무관하게 **INFO** 로 남긴다. audit 의 목적은 "무슨 일이 있었는지"를
// 빠짐없이 남기는 것이라 실패만 올리면 성공 대비 비율을 로그만으로 계산할 수 없다.
// 실패 자체에 대한 WARN 은 호출부가 이미 따로 남긴다.
func (g *Generator) auditRecord(
	log *logger.Logger,
	host string,
	targetType model.TargetType,
	sampleURL, modelName string,
	selectors model.SelectorMap,
	startedAt time.Time,
	err error,
) {
	auditResult, metricStatus := classifyOutcome(err)
	elapsed := time.Since(startedAt)

	fields := map[string]interface{}{
		"audit":       "llm_rule_generation",
		"host":        host,
		"target_type": string(targetType),
		"url":         sampleURL,
		"result":      auditResult,
		"duration_ms": elapsed.Milliseconds(),
	}
	if modelName != "" {
		fields["model"] = modelName
	}
	if auditResult == GenStatusSuccess {
		if fp := selectorFingerprint(selectors); fp != "" {
			fields["selector_hash"] = fp
		}
	}
	if err != nil {
		fields["error_detail"] = err.Error()
	}
	log.WithFields(fields).Info(auditEvent)

	// 정의만 되고 기록되지 않던 상태를 여기서 함께 메운다 (이슈 #583).
	//
	// GenStatusValidationFail / GenStatusLLMError 는 metrics.go 에 상수로 존재했으나
	// RecordCall 호출이 없어 **실패가 지표에 잡히지 않았다** — 성공과 cap_exceeded 만
	// 세어져 실패율을 볼 수 없었다. audit 이 이미 모든 분기를 지나므로 같은 지점에서
	// 기록해 분기 누락이 재발하지 않게 한다.
	if metricStatus != "" {
		g.genMetrics.RecordCall(metricStatus, elapsed.Seconds())
	}
}
