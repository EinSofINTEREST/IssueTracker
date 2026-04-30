// Package pathinfer 는 같은 호스트의 sample URL 들로부터 path_pattern regex 를
// 알고리즘 기반으로 추론합니다 (이슈 #173 단계 2).
//
// 본 패키지는 **LLM 의존 없는 결정적 휴리스틱** 만 다룹니다 — 70-80% 의 단순 ID
// 패턴 (numeric/UUID/slug/연도-월) 케이스를 cover. 모호한 케이스는 ok=false 로
// 반환하여 호출자가 LLM 기반 추론 (단계 3) 또는 host-only catch-all 유지로 분기.
//
// 위치 결정 (이슈 본문은 llmgen/pathinfer 제안):
//
//	본 패키지는 LLM 무관 — llmgen 패키지 아래 두면 의존 분리 깨짐.
//	단계 3 (LLM 추론) 이 도입되면 llmgen 측에서 본 패키지를 import 하여 알고리즘 우선
//	시도 + 실패 시 LLM fallback 으로 합성하는 hybrid 흐름 구성.
//
// Algorithm 개요 (InferHeuristic 참조):
//  1. 입력 sample URL path 들을 segment 슬라이스로 분리 ("/article/123" → ["article", "123"]).
//  2. segment 길이가 모두 같지 않으면 ok=false (구조적 변형 — 휴리스틱으로 처리 어려움).
//  3. 각 segment index 별로:
//     - 모든 sample 에서 동일 텍스트 → 정적 prefix (정규식 escape 후 그대로 사용).
//     - 다르면 → 변수 부분: 각 sample 의 동일 패턴 인식 (\\d+ / UUID / slug / 연도) →
//     공통 패턴 발견 시 그 regex 사용. 모두 다른 종류면 ok=false.
//  4. 결과 regex: "^/static/(\d+)/(\d+)$" 형태로 합성.
//  5. 매칭 검증: 모든 입력 sample 이 결과 regex 에 매칭되는지 확인 — 실패 시 ok=false.
package pathinfer

import (
	"regexp"
	"strings"
)

// minSamplesForInference 는 휴리스틱이 의미 있는 패턴을 추출하기 위한 최소 sample 수입니다.
// 1-2 개로는 \"공통 prefix vs 변수 부분\" 구분이 의미 없음 (모든 segment 가 \"공통\" 으로 보임).
// 3+ 개면 변수 부분의 다양성을 검출 가능.
const minSamplesForInference = 3

// PathSamples 는 휴리스틱 추론에 사용할 입력입니다.
//
// Articles 는 같은 호스트 / 같은 페이지 종류의 article URL path 슬라이스 — 정규화된 URL 의
// path 부분만 (예: \"/article/12345\"). scheme / host 제거 권장.
type PathSamples struct {
	Articles []string
}

// InferHeuristic 은 PathSamples 로부터 path_pattern regex 를 추론합니다 (이슈 #173 단계 2).
//
// 반환값:
//   - regex string: 결과 RE2 패턴 (^...$ 앵커 포함). ok=false 면 빈 문자열.
//   - ok bool     : 신뢰도. 다음 조건 중 하나라도 만족하지 않으면 false:
//     1. samples 가 minSamplesForInference 미만
//     2. samples 의 segment 길이가 일정하지 않음
//     3. 변수 부분 segment 에서 공통 패턴을 찾지 못함
//     4. 결과 regex 가 입력 samples 전체를 매칭하지 못함
//
// 결정성: 동일 입력 → 동일 출력. test 친화 + 운영 디버깅 용이.
func InferHeuristic(samples PathSamples) (string, bool) {
	if len(samples.Articles) < minSamplesForInference {
		return "", false
	}

	// 모든 sample 의 segment 분리.
	segs := make([][]string, len(samples.Articles))
	for i, p := range samples.Articles {
		segs[i] = splitSegments(p)
	}

	// segment 개수가 모두 같은지 확인.
	expected := len(segs[0])
	for _, s := range segs {
		if len(s) != expected {
			return "", false
		}
	}

	// 각 segment index 별로 정적 prefix 또는 변수 부분 결정.
	parts := make([]string, expected)
	for idx := 0; idx < expected; idx++ {
		// 해당 index 의 모든 값 수집.
		values := make([]string, len(segs))
		for s := range segs {
			values[s] = segs[s][idx]
		}

		if allEqual(values) {
			// 정적 prefix — regex escape 후 그대로 사용.
			parts[idx] = regexp.QuoteMeta(values[0])
			continue
		}

		// 변수 부분 — 공통 패턴 추출.
		regexFragment, ok := inferVariablePattern(values)
		if !ok {
			return "", false
		}
		parts[idx] = regexFragment
	}

	// 결과 regex 합성. URL path 는 항상 "/" 로 시작 — leading "/" 안전 prefix.
	result := "^/" + strings.Join(parts, "/") + "$"

	// 검증: 입력 samples 전체가 결과 regex 에 매칭되는지 확인 — hallucination 방어.
	re, err := regexp.Compile(result)
	if err != nil {
		return "", false
	}
	for _, p := range samples.Articles {
		if !re.MatchString(p) {
			return "", false
		}
	}

	return result, true
}

// splitSegments 는 path 를 segment 슬라이스로 분리합니다.
// "/article/123" → ["article", "123"]. trailing "/" 제거. 빈 path 는 [].
func splitSegments(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// allEqual 은 슬라이스의 모든 값이 동일한지 확인합니다.
func allEqual(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i] != values[0] {
			return false
		}
	}
	return true
}

// inferVariablePattern 은 변수 부분의 공통 패턴을 추출합니다.
//
// 우선순위 (가장 구체적 → 일반적) — 더 좁은 범위 패턴이 먼저 평가되어야 일반 numeric 으로 잡히지 않음:
//  1. UUID (가장 specific 한 형식)
//  2. 연도 ((19|20)\d{2}) — 4자리 numeric 중 1900-2099 범위만
//  3. 월 (0[1-9]|1[0-2]) — 2자리 numeric 중 01-12 범위만
//  4. 일반 numeric (\d+) — 위 범위 패턴 미적용 시 fallback
//  5. slug ([a-z0-9-]+, 4자 이상 + 하이픈 포함)
//
// 위 어느 패턴에도 해당 안 되면 ok=false (LLM 추론 또는 catch-all 로 fallback).
func inferVariablePattern(values []string) (string, bool) {
	switch {
	case allMatch(values, regexUUID):
		return `([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`, true
	case allMatch(values, regexYear):
		return `(19|20)\d{2}`, true
	case allMatch(values, regexMonth):
		return `(0[1-9]|1[0-2])`, true
	case allMatch(values, regexNumeric):
		return `(\d+)`, true
	case allSlug(values):
		return `([a-z0-9-]+)`, true
	}
	return "", false
}

// 사전 컴파일된 패턴 — 매 호출마다 재컴파일 회피.
var (
	regexNumeric = regexp.MustCompile(`^\d+$`)
	regexUUID    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// 연도: 1900-2099 범위 보호 (단순 \d{4} 는 9999 같은 무의미 값 허용 — 본 휴리스틱은
	// year/month 가 실제 발견되는 사이트 (예: /news/2024/04/...) 범위로 한정).
	regexYear  = regexp.MustCompile(`^(19|20)\d{2}$`)
	regexMonth = regexp.MustCompile(`^(0[1-9]|1[0-2])$`)
	// slug 보조 — 4자 이상 + 하이픈 포함 (단순 영숫자 4자 = ID 일 가능성 vs 하이픈 있으면
	// 명확한 slug). 단순 숫자 / UUID 와의 우선순위 분리.
	regexSlugChars = regexp.MustCompile(`^[a-z0-9-]+$`)
)

// allMatch 는 모든 값이 주어진 패턴에 매칭되는지 확인합니다.
func allMatch(values []string, re *regexp.Regexp) bool {
	for _, v := range values {
		if !re.MatchString(v) {
			return false
		}
	}
	return true
}

// allSlug 는 모든 값이 slug 형식 (소문자 영숫자 + 하이픈, 4자 이상, 하이픈 포함) 인지 확인합니다.
//
// 4자 이상 + 하이픈 포함 조건은 단순 short ID (예: "abc") 나 단순 영숫자 (UUID 부분 or hash)
// 와 구분하기 위함 — 명확한 slug 만 잡아내고 모호한 케이스는 ok=false 로 LLM/catch-all 위임.
func allSlug(values []string) bool {
	for _, v := range values {
		if !regexSlugChars.MatchString(v) {
			return false
		}
		if len(v) < 4 {
			return false
		}
		if !strings.Contains(v, "-") {
			return false
		}
	}
	return true
}
