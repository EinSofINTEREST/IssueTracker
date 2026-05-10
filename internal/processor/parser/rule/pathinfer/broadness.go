package pathinfer

import (
	"os"
	"regexp"
	"strings"
)

// broadnessCheckEnvVar 는 broadness 휴리스틱의 fail-safe off 토글입니다 (이슈 #311).
//
// 운영 정책:
//   - 미설정 / 빈 값 / 그 외   → enabled (default true)
//   - "false" / "0" / "no"     → disabled
//
// case-insensitive. 정상 정밀화가 broadness 휴리스틱에 의해 거부될 때 운영자가 빠르게 차단 해제
// 가능 — 코드 변경 / 재배포 없이 환경변수만 갱신.
const broadnessCheckEnvVar = "PATHINFER_BROADNESS_CHECK"

// broadnessCheckEnabled 는 PATHINFER_BROADNESS_CHECK 환경변수를 기준으로 휴리스틱 활성 여부를 반환합니다.
func broadnessCheckEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(broadnessCheckEnvVar)))
	switch v {
	case "false", "0", "no":
		return false
	default:
		return true
	}
}

// validateBroadness 는 sample-driven 휴리스틱으로 pattern 의 구체성 (specificity) 을 검증합니다 (이슈 #311).
//
// triviallyBroad 화이트리스트 12 케이스 외에 "약간만 좁은" broad 패턴 (예: ^/.+/\d+$, ^/[^/]+/\d+$)
// 도 거부하기 위한 추가 안전망. positive samples 의 구조적 특성을 기반으로 합성 negative path 를
// 만들어 pattern 이 그것까지 매칭하면 broad 로 판정.
//
// 검증 단계:
//  1. Segment 수 일치 — positive 가 모두 K segment 면, K±1 segment path 도 매칭하면 거부
//  2. Literal segment 보존 — positive 모두에 동일한 segment value 가 있으면, 그 자리에 임의 token
//     을 넣은 path 가 매칭되면 거부 (예: 모든 positive 가 /article/ 시작이면 /broadnesstoken/X 거부)
//
// 두 단계 모두 sample-driven — positive 가 적거나 segment 수가 들쑥날쑥하면 fail-open (true 반환).
//
// re 가 nil 이면 panic — 호출자가 RE2 컴파일 후 전달.
func validateBroadness(re *regexp.Regexp, samples LLMSamples) bool {
	if re == nil {
		panic("pathinfer: validateBroadness called with nil regexp")
	}
	if len(samples.Articles) == 0 {
		return true
	}

	// positives 를 segment array 로 normalize. pathinfer.go 의 splitSegments 재사용.
	positives := make([][]string, 0, len(samples.Articles))
	for _, a := range samples.Articles {
		positives = append(positives, splitSegments(a))
	}

	if !validateSegmentCount(re, positives) {
		return false
	}
	if !validateLiteralSegments(re, positives) {
		return false
	}
	return true
}

// validateSegmentCount 는 positive 가 모두 동일 segment 수 K 인 경우, pattern 이 K-1 / K+1
// segment path 도 매칭하면 false 반환 (broad).
//
// positive 가 들쑥날쑥하면 (예: 2 + 3 segments) skip — 정확성 보장 못해 fail-open.
func validateSegmentCount(re *regexp.Regexp, positives [][]string) bool {
	if len(positives) == 0 {
		return true
	}
	k := len(positives[0])
	for _, p := range positives[1:] {
		if len(p) != k {
			return true // 들쑥날쑥 — skip
		}
	}
	if k == 0 {
		return true // 모두 root path
	}

	base := positives[0]

	// K+1 segment 변형 (prepend): pattern 이 "/" 시작이 아닌 token 으로 확장된 path 도 허용하면 broad.
	longerPrepend := "/broadnesstoken/" + strings.Join(base, "/")
	if re.MatchString(longerPrepend) {
		return false
	}

	// K+1 segment 변형 (append): 끝 segment 추가. ^/article/\d+(/.+)?$ 같은 trailing-optional
	// 패턴이 K+1 까지 허용하는 케이스를 거부.
	longerAppend := "/" + strings.Join(base, "/") + "/broadnesstoken"
	if re.MatchString(longerAppend) {
		return false
	}

	// K-1 segment 변형: 앞 / 뒤 양쪽에서 한 segment drop (CodeRabbit Major 반영).
	// optional leading group (예: ^(/lang)?/article/\d+$) 같은 패턴이 K-1 으로 통과하는 케이스도 거부.
	// K=1 이면 "/" 가 되므로 의미 없는 경우 skip.
	if k >= 2 {
		shorterFront := "/" + strings.Join(base[1:], "/")
		if re.MatchString(shorterFront) {
			return false
		}
		shorterBack := "/" + strings.Join(base[:k-1], "/")
		if re.MatchString(shorterBack) {
			return false
		}
	}

	return true
}

// validateLiteralSegments 는 positive 들이 같은 자리에 같은 값을 가지는 (constant) segment 가 있을 때,
// 그 자리에 임의 token 을 넣은 path 가 매칭되면 false 반환 (broad).
//
// positive 가 1개만 있으면 모든 segment 가 "constant" 처럼 보이므로 검증 skip — 정상 capture
// (예: \d+) 까지 거부할 위험.
//
// segment 수가 들쑥날쑥해도 정확한 비교 불가 — skip.
func validateLiteralSegments(re *regexp.Regexp, positives [][]string) bool {
	if len(positives) < 2 {
		return true
	}
	k := len(positives[0])
	for _, p := range positives[1:] {
		if len(p) != k {
			return true
		}
	}
	if k == 0 {
		return true
	}

	// 모든 positive 가 동일 값을 갖는 segment 인덱스 찾기.
	constantIdx := make([]int, 0, k)
	for i := 0; i < k; i++ {
		v := positives[0][i]
		constant := true
		for _, p := range positives[1:] {
			if p[i] != v {
				constant = false
				break
			}
		}
		if constant {
			constantIdx = append(constantIdx, i)
		}
	}

	if len(constantIdx) == 0 {
		return true // literal 신호 없음
	}

	// constant segment 자리에 임의 token 을 넣어 변형 path 생성. pattern 이 매칭하면 broad.
	// 다양한 token 으로 character class 변형도 흡수 — 숫자 only 인 [0-9]+ 같은 좁은 capture 는
	// alphanumeric token 에서 매칭 안 되어 정상 통과.
	tokens := []string{"broadnesstoken", "ZZZZZZZZZ", "different-host"}
	base := make([]string, k)
	copy(base, positives[0])
	for _, idx := range constantIdx {
		for _, tok := range tokens {
			// constant value 와 동일한 token 은 substitution 효과 없음 → false rejection 회피
			// (CodeRabbit Minor 반영).
			if tok == base[idx] {
				continue
			}
			variant := make([]string, k)
			copy(variant, base)
			variant[idx] = tok
			test := "/" + strings.Join(variant, "/")
			if re.MatchString(test) {
				return false
			}
		}
	}

	return true
}
