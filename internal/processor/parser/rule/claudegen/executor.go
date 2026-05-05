// Package claudegen 은 Claude Code Docker 컨테이너를 실행하여 HTML 에서 CSS 셀렉터를
// 생성하는 컴포넌트입니다 (이슈 #256).
//
// 역할: 기존 llmgen.Generator 가 Gemini Flash 로 수행하던 셀렉터 추출 단계를 대체.
// 검증은 validator.ValidatorPool (이슈 #257) 이 담당.
//
// 환경변수:
//   - ANTHROPIC_API_KEY  : Claude Code 인증 (필수)
//   - CLAUDE_CODE_MODEL  : 모델 ID (기본: claude-sonnet-4-6)
//   - CLAUDE_CODE_IMAGE  : Docker 이미지 (기본: ghcr.io/anthropics/claude-code:latest)
//   - CLAUDE_CODE_TIMEOUT: 단일 실행 타임아웃 (기본: 120s)
package claudegen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

const (
	defaultImage   = "ghcr.io/anthropics/claude-code:latest"
	defaultModel   = "claude-sonnet-4-6"
	defaultTimeout = 120 * time.Second
)

// DockerRunner 는 docker 명령 실행을 추상화합니다 (테스트에서 mock 교체 용).
// env 는 컨테이너 프로세스에 추가할 환경변수 목록 — API 키 등 비밀값은 args 가 아닌
// 이 슬라이스로 전달해 ps/proc 노출을 방지합니다 (Gemini Security + Copilot 반영).
type DockerRunner interface {
	Run(ctx context.Context, args []string, env []string) (stdout string, stderr string, err error)
}

// execDockerRunner 는 실제 docker CLI 를 실행하는 기본 구현입니다.
type execDockerRunner struct{}

func (r *execDockerRunner) Run(ctx context.Context, args []string, env []string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// 비밀값(API 키 등)은 cmd.Env 를 통해 주입 — args 에 포함 시 ps/procfs 로 노출됨.
	cmd.Env = append(os.Environ(), env...)
	return stdout.String(), stderr.String(), cmd.Run()
}

// Executor 는 Claude Code Docker 를 실행하여 CSS 셀렉터를 추출합니다.
type Executor struct {
	image   string
	model   string
	apiKey  string
	timeout time.Duration
	runner  DockerRunner
	log     *logger.Logger
}

// ModelName 은 이 Executor 가 사용하는 모델 ID 를 반환합니다.
// llmgen.Generator 가 DB description 에 기록할 때 사용합니다 (이슈 #256).
func (e *Executor) ModelName() string { return e.model }

// NewFromEnv 는 환경변수 기반 Executor 를 생성합니다.
//
//	CLAUDE_CODE_IMAGE   — Docker 이미지 (기본: ghcr.io/anthropics/claude-code:latest)
//	CLAUDE_CODE_MODEL   — 모델 ID     (기본: claude-sonnet-4-6)
//	ANTHROPIC_API_KEY   — 인증 키     (필수)
//	CLAUDE_CODE_TIMEOUT — 타임아웃    (기본: 120s, Go duration 형식)
func NewFromEnv(log *logger.Logger) (*Executor, error) {
	if log == nil {
		return nil, errors.New("claudegen: NewFromEnv requires non-nil logger")
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("claudegen: ANTHROPIC_API_KEY is required")
	}
	timeout := defaultTimeout
	if s := os.Getenv("CLAUDE_CODE_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			log.WithFields(map[string]interface{}{
				"value": s,
			}).WithError(err).Warn("CLAUDE_CODE_TIMEOUT parse failed, using default")
		} else {
			timeout = d
		}
	}
	return &Executor{
		image:   envOr("CLAUDE_CODE_IMAGE", defaultImage),
		model:   envOr("CLAUDE_CODE_MODEL", defaultModel),
		apiKey:  apiKey,
		timeout: timeout,
		runner:  &execDockerRunner{},
		log:     log,
	}, nil
}

// New 는 명시적 파라미터로 Executor 를 생성합니다 (DI 용).
func New(image, model, apiKey string, timeout time.Duration, log *logger.Logger) (*Executor, error) {
	if log == nil {
		return nil, errors.New("claudegen: New requires non-nil logger")
	}
	return &Executor{image: image, model: model, apiKey: apiKey, timeout: timeout, runner: &execDockerRunner{}, log: log}, nil
}

// NewWithRunner 는 DockerRunner 를 주입하는 생성자입니다 (테스트/DI 용).
func NewWithRunner(image, model, apiKey string, timeout time.Duration, runner DockerRunner, log *logger.Logger) (*Executor, error) {
	if log == nil {
		return nil, errors.New("claudegen: NewWithRunner requires non-nil logger")
	}
	return &Executor{image: image, model: model, apiKey: apiKey, timeout: timeout, runner: runner, log: log}, nil
}

// Extract 는 HTML 을 Docker 컨테이너에 마운트하고 Claude Code 로 셀렉터를 추출합니다.
//
// 실행 흐름:
//  1. HTML 을 임시 디렉토리에 page.html 로 기록
//  2. docker run --rm 으로 컨테이너 실행 (/workspace 마운트)
//  3. Claude Code -p <prompt> 로 셀렉터 JSON 생성
//  4. stdout 에서 JSON 파싱 후 SelectorMap 반환
func (e *Executor) Extract(ctx context.Context, host string, targetType storage.TargetType, html string) (storage.SelectorMap, error) {
	tmpDir, err := os.MkdirTemp("", "claudegen-*")
	if err != nil {
		return storage.SelectorMap{}, fmt.Errorf("create tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "page.html"), []byte(html), 0o644); err != nil {
		return storage.SelectorMap{}, fmt.Errorf("write html: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// ANTHROPIC_API_KEY 는 args 가 아닌 env 슬라이스로 전달 — ps/proc 노출 방지 (보안).
	// docker args 에는 `-e ANTHROPIC_API_KEY` (값 없이) 만 포함하여 컨테이너가 부모 환경에서 상속.
	args := []string{
		"run", "--rm",
		"-e", "ANTHROPIC_API_KEY",
		"-v", tmpDir + ":/workspace:ro",
		e.image,
		"--model", e.model,
		"--dangerously-skip-permissions",
		"-p", buildPrompt(host, targetType),
	}
	env := []string{"ANTHROPIC_API_KEY=" + e.apiKey}

	e.log.WithFields(map[string]interface{}{
		"host":        host,
		"target_type": string(targetType),
		"image":       e.image,
		"model":       e.model,
	}).Debug("running claude code extractor")

	stdout, stderr, err := e.runner.Run(runCtx, args, env)
	if err != nil {
		return storage.SelectorMap{}, fmt.Errorf("claude code docker run: %w (stderr: %s)",
			err, truncate(stderr, 512))
	}

	sm, err := parseSelectorOutput(stdout)
	if err != nil {
		// raw stdout 은 debug 로그에만 기록 — 에러 메시지 포함 시 민감정보 노출 가능 (Copilot 반영).
		e.log.WithFields(map[string]interface{}{
			"host":        host,
			"target_type": string(targetType),
			"raw_output":  truncate(stdout, 256),
		}).Debug("claude code output parse failed")
		return storage.SelectorMap{}, fmt.Errorf("parse claude output: %w", err)
	}

	e.log.WithFields(map[string]interface{}{
		"host":        host,
		"target_type": string(targetType),
	}).Info("claude code extractor succeeded")

	return sm, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// parseSelectorOutput 은 Claude Code 출력에서 JSON 블록을 추출하고 SelectorMap 으로 파싱합니다.
func parseSelectorOutput(output string) (storage.SelectorMap, error) {
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		return storage.SelectorMap{}, fmt.Errorf("no JSON object found in output")
	}
	var sm storage.SelectorMap
	if err := json.Unmarshal([]byte(jsonStr), &sm); err != nil {
		return storage.SelectorMap{}, fmt.Errorf("unmarshal selector map: %w", err)
	}
	return sm, nil
}

// extractJSON 은 출력 텍스트에서 첫 번째 유효한 {...} JSON 블록을 추출합니다.
// JSON 문자열 내부의 중괄호(CSS selector 내 :contains('{') 등)는 depth 계산에서 제외합니다.
func extractJSON(s string) string {
	start := -1
	depth := 0
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start != -1 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
