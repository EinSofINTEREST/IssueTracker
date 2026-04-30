// Package prompt loads LLM prompt templates from the filesystem.
//
// Package prompt 는 LLM 호출용 프롬프트를 외부 파일에서 로드합니다 (이슈 #144 Phase 4).
//
// **위치 정책**: 프롬프트는 binary 와 분리하여 \`scripts/prompts/<name>.txt\` (또는 .md) 로 관리합니다.
// 이는 다음을 가능하게 합니다:
//   - 운영 중 프롬프트만 변경 (재빌드 없이)
//   - PR 리뷰 시 프롬프트 변경이 코드와 별도로 잘 보임
//   - 비-Go 운영자도 프롬프트 편집 가능
//
// **path 결정**: 환경변수 \`ISSUETRACKER_PROMPTS_DIR\` 가 set 되어 있으면 그 디렉토리, 아니면
// \`scripts/prompts\` (현재 워킹 디렉토리 기준 상대 경로 — 일반적으로 repo root 에서 실행).
//
// 사용 예시:
//
//	body, err := prompt.Load("classify_news")  // scripts/prompts/classify_news.txt 또는 .md
//	if err != nil { log.Fatal(err) }
//	resp, _ := provider.Generate(ctx, llm.Request{
//	    Messages: []llm.Message{{Role: llm.RoleUser, Content: body}},
//	    TaskHint: llm.TaskHintReasoning,
//	})
package prompt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnvPromptsDir 는 prompts 디렉토리를 override 하는 환경변수 이름입니다.
const EnvPromptsDir = "ISSUETRACKER_PROMPTS_DIR"

// DefaultDir 는 환경변수 미설정 시 사용하는 prompts 디렉토리 (cwd 기준 상대 경로).
const DefaultDir = "scripts/prompts"

// supportedExtensions 는 Load 시 자동 시도되는 파일 확장자 — 우선순위 순.
var supportedExtensions = []string{".txt", ".md"}

// ErrNotFound 는 어떤 확장자로도 prompt 파일을 찾지 못했을 때 반환됩니다.
var ErrNotFound = errors.New("prompt not found")

// Load returns the contents of the prompt file with the given name (확장자 제외).
//
// 검색 순서:
//  1. <dir>/<name>.txt
//  2. <dir>/<name>.md
//
// 모두 미존재 시 ErrNotFound 반환. dir 은 EnvPromptsDir 환경변수 또는 DefaultDir.
//
// name 에 path separator 가 포함되면 ErrNotFound (path traversal 방지) — 단순 식별자만 허용.
func Load(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("prompt name must not be empty")
	}
	if filepath.Base(name) != name {
		return "", fmt.Errorf("prompt name %q must not contain path separators", name)
	}

	dir := dirFromEnv()
	for _, ext := range supportedExtensions {
		path := filepath.Join(dir, name+ext)
		body, err := os.ReadFile(path)
		if err == nil {
			return string(body), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read prompt %q: %w", path, err)
		}
	}
	return "", fmt.Errorf("%w: %s (dir=%s)", ErrNotFound, name, dir)
}

// dirFromEnv returns the prompts directory — env override or default.
func dirFromEnv() string {
	if v := os.Getenv(EnvPromptsDir); v != "" {
		return v
	}
	return DefaultDir
}
