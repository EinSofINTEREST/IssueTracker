package prompt

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// placeholderPattern 은 prompt 템플릿의 치환 토큰 (`{{UPPER_SNAKE}}`) 을 추출합니다.
//
// Render 가 strings.NewReplacer 기반 단순 치환이라 템플릿에만 존재하고 호출자가 공급하지 않는
// 토큰은 **치환되지 않은 채 그대로 LLM 에 전달** 됩니다. 이를 사전에 잡기 위한 패턴.
var placeholderPattern = regexp.MustCompile(`\{\{[A-Z0-9_]+\}\}`)

// Contract 는 prompt asset 과 그것을 렌더링하는 호출자 사이의 계약입니다.
//
// Placeholders 에는 호출자가 Render 에 넘기는 토큰을 **빠짐없이** 나열합니다. 템플릿이 그
// 목록에 없는 토큰을 쓰고 있으면 Verify 가 실패시킵니다 (반대 방향 — 목록에 있으나 템플릿이
// 쓰지 않는 토큰 — 은 무해한 no-op 이라 허용).
type Contract struct {
	// Name 은 Loader.Load 에 넘기는 prompt 이름 (예: "parser/llmgen/system").
	Name string
	// Placeholders 는 호출자가 공급하는 토큰 목록 (예: "{{HOST}}"). 없으면 nil.
	Placeholders []string
}

// Verify 는 contracts 의 모든 prompt 를 실제로 로드하고 placeholder 계약을 검증합니다.
//
// 두 가지를 동시에 잡습니다.
//
//  1. **로드 실패** — 이름 오타나 LLM_PROMPT_DIR override 시 파일 누락. Loader 는 lazy 라
//     검증이 없으면 해당 stage 의 첫 실제 요청에서야 드러나고, 그 시점엔 이미 파이프라인이
//     흐르는 중입니다.
//  2. **미공급 placeholder** — 템플릿이 쓰는 토큰을 호출자가 공급하지 않는 경우. 치환되지
//     않은 `{{FOO}}` 가 그대로 LLM 에 전달되어 응답 품질이 조용히 무너지고 호출 비용만 나갑니다.
//
// 모든 문제를 모아 errors.Join 으로 반환 — 운영자가 한 번에 전부 보고 고칠 수 있도록.
// contracts 가 비어 있으면 nil (검증할 대상 없음).
func Verify(l Loader, contracts []Contract) error {
	if l == nil {
		return errors.New("prompt: Verify requires non-nil loader")
	}

	var problems []error
	for _, c := range contracts {
		if c.Name == "" {
			problems = append(problems, errors.New("prompt: contract with empty name"))
			continue
		}

		body, err := l.Load(c.Name)
		if err != nil {
			problems = append(problems, fmt.Errorf("prompt %q: load failed: %w", c.Name, err))
			continue
		}

		if missing := unsuppliedPlaceholders(body, c.Placeholders); len(missing) > 0 {
			problems = append(problems, fmt.Errorf(
				"prompt %q: template uses placeholder(s) the caller does not supply: %s",
				c.Name, strings.Join(missing, ", ")))
		}
	}
	return errors.Join(problems...)
}

// unsuppliedPlaceholders 는 템플릿이 쓰지만 supplied 에 없는 토큰을 정렬해 반환합니다.
//
// 중복 토큰은 1회만 보고 — 운영자 에러 메시지가 같은 이름으로 길어지지 않도록.
func unsuppliedPlaceholders(template string, supplied []string) []string {
	found := placeholderPattern.FindAllString(template, -1)
	if len(found) == 0 {
		return nil
	}

	ok := make(map[string]struct{}, len(supplied))
	for _, s := range supplied {
		ok[s] = struct{}{}
	}

	seen := make(map[string]struct{}, len(found))
	var missing []string
	for _, token := range found {
		if _, supplied := ok[token]; supplied {
			continue
		}
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		missing = append(missing, token)
	}
	sort.Strings(missing)
	return missing
}

// ContractNames 는 contracts 의 이름만 추출합니다 — 로깅 / 테스트 보조용.
func ContractNames(contracts []Contract) []string {
	names := make([]string, 0, len(contracts))
	for _, c := range contracts {
		names = append(names, c.Name)
	}
	return names
}
