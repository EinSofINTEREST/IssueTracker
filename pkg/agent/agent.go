// Package agent 는 vendor-agnostic 한 CLI 기반 agent backend 의 공통 추상을 제공합니다 (이슈 #460).
//
// 디자인 목표:
//   - claude / codex 등 어떤 CLI agent 든 동일 인터페이스로 호출
//   - 각 stage (parser / enrich / ...) 는 concrete agent 를 모름 — Agent 인터페이스만 의존
//   - main.go 에서 concrete agent 를 DI — caller / stage 변경 없이 agent backend swap 가능
//
// 본 패키지는 인터페이스만 정의 — concrete 구현은 sub-package:
//   - pkg/agent/claude — Claude Code CLI backend (이슈 #458 / PR #459)
//   - pkg/agent/codex  — OpenAI Codex CLI backend (후속 이슈)
package agent

import "context"

// Agent 는 prompt + files 를 받아 stdout 텍스트를 반환하는 CLI 기반 LLM agent 입니다.
//
// 시그니처:
//   - sessionLabel: 로그·디버그용 라벨 (예: "enrich-extract", "parser-rule-gen")
//   - files       : 세션 작업 디렉토리에 기록할 파일들 (filename → bytes). prompt 가 참조.
//   - prompt      : caller 가 이미 렌더링 완료한 최종 prompt 텍스트
//
// 반환: stdout 텍스트 (caller 가 자체 schema 로 파싱). JSON 파싱 / blacklist 등 도메인 분기는
// 호출자 책임 — Agent 는 transport 만 담당.
//
// nil files 허용 — 파일 없이 prompt 만 보내는 케이스 지원.
// 호출 timeout 은 caller 가 ctx 로 부여 (Agent 내부에서 별도 timeout 정책 X).
type Agent interface {
	RunSession(
		ctx context.Context,
		sessionLabel string,
		files map[string][]byte,
		prompt string,
	) (stdout string, err error)
}
