// Package providers 는 모든 내장 provider 를 사이드 이펙트로 등록하는 편의 패키지입니다 (이슈 #140).
//
// 사용자는 호출처에서 다음 한 줄만 import 하면 모든 provider (gemini / openai / anthropic / claude) 가
// llm.New 에 자동 등록됩니다:
//
//	import _ "issuetracker/pkg/llm/providers"
//
// 특정 provider 만 사용하고 싶다면 본 패키지 대신 해당 provider 패키지만 직접 import 하면 됩니다.
package providers

import (
	_ "issuetracker/pkg/llm/anthropic"
	_ "issuetracker/pkg/llm/gemini"
	_ "issuetracker/pkg/llm/openai"
)
