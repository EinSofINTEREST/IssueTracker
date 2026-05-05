// Package claudegen 은 상시 기동된 Claude Code Docker 컨테이너에 세션을 생성하여
// HTML 에서 CSS 셀렉터를 추출하는 컴포넌트입니다 (이슈 #256).
//
// 기존 콜드스타트 방식(docker run --rm)과 달리, 컨테이너를 서비스 기동 시 한 번만 띄우고
// (Start) 요청마다 docker exec 으로 새 세션을 생성합니다. 컨테이너 초기화 비용을 최초 1회로
// 상각하여 이후 요청의 레이턴시를 줄입니다.
//
// 환경변수:
//   - ANTHROPIC_API_KEY    : Claude Code 인증 (필수)
//   - CLAUDE_CODE_MODEL    : 모델 ID (기본: claude-sonnet-4-6)
//   - CLAUDE_CODE_IMAGE    : Docker 이미지 (기본: ghcr.io/anthropics/claude-code:latest)
//   - CLAUDE_CODE_TIMEOUT  : 세션 단위 타임아웃 (기본: 120s, Go duration 형식)
package claudegen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

const (
	defaultImage          = "ghcr.io/anthropics/claude-code:latest"
	defaultModel          = "claude-sonnet-4-6"
	defaultSessionTimeout = 120 * time.Second

	truncateStderrLen = 512 // exec 실패 시 stderr 미리보기 최대 길이
	truncateStdoutLen = 256 // 파싱 실패 시 stdout 미리보기 최대 길이
)

// ContainerRunner 는 Docker 컨테이너 생명주기를 추상화합니다 (테스트 mock 교체용).
//
// API 키 등 비밀값은 env 슬라이스로 전달 — args 에 포함 시 ps/procfs 로 노출됩니다.
type ContainerRunner interface {
	// StartContainer 는 장기 실행 컨테이너를 기동하고 컨테이너 ID 를 반환합니다.
	// workDir 은 컨테이너의 /workspace 로 마운트할 호스트 경로입니다.
	StartContainer(ctx context.Context, image, workDir string, env []string) (containerID string, err error)

	// ExecSession 은 실행 중인 컨테이너에서 명령을 실행합니다.
	ExecSession(ctx context.Context, containerID string, args []string) (stdout, stderr string, err error)

	// StopContainer 는 컨테이너를 강제 종료 + 삭제합니다.
	StopContainer(ctx context.Context, containerID string) error
}

// ClaudeWorker 는 상시 기동된 Claude Code 컨테이너에 세션을 생성해 CSS 셀렉터를 추출합니다.
//
// Lifecycle:
//   - Start(ctx): 컨테이너 기동 + workspace 디렉토리 생성
//   - Extract(ctx, ...): 세션별 고유 서브디렉토리 생성 → docker exec → JSON 파싱
//   - Stop(ctx): 컨테이너 종료 + workspace 정리
//
// Extract 는 동시 호출에 안전합니다 — 각 세션이 고유 디렉토리를 사용합니다.
// Stop 은 진행 중인 모든 Extract 완료를 대기합니다 (graceful shutdown).
type ClaudeWorker struct {
	image          string
	model          string
	apiKey         string
	sessionTimeout time.Duration
	runner         ContainerRunner
	log            *logger.Logger

	mu          sync.RWMutex
	containerID string
	workDir     string
	wg          sync.WaitGroup // 진행 중인 Extract 호출 추적
}

// ModelName 은 이 Worker 가 사용하는 모델 ID 를 반환합니다.
// llmgen.Generator 가 DB description 에 기록할 때 사용합니다 (이슈 #256).
func (w *ClaudeWorker) ModelName() string { return w.model }

// NewFromEnv 는 환경변수 기반 ClaudeWorker 를 생성합니다.
// Start() 를 호출하기 전까지는 컨테이너가 기동되지 않습니다.
func NewFromEnv(log *logger.Logger) (*ClaudeWorker, error) {
	if log == nil {
		return nil, errors.New("claudegen: NewFromEnv requires non-nil logger")
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, errors.New("claudegen: ANTHROPIC_API_KEY is required")
	}
	timeout := defaultSessionTimeout
	if s := os.Getenv("CLAUDE_CODE_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			log.WithFields(map[string]interface{}{"value": s}).WithError(err).
				Warn("CLAUDE_CODE_TIMEOUT parse failed, using default")
		} else if d <= 0 {
			log.WithFields(map[string]interface{}{"value": s}).
				Warn("CLAUDE_CODE_TIMEOUT must be positive, using default")
		} else {
			timeout = d
		}
	}
	return &ClaudeWorker{
		image:          envOr("CLAUDE_CODE_IMAGE", defaultImage),
		model:          envOr("CLAUDE_CODE_MODEL", defaultModel),
		apiKey:         apiKey,
		sessionTimeout: timeout,
		runner:         &execContainerRunner{},
		log:            log,
	}, nil
}

// New 는 명시적 파라미터로 ClaudeWorker 를 생성합니다 (DI 용).
func New(image, model, apiKey string, timeout time.Duration, log *logger.Logger) (*ClaudeWorker, error) {
	if log == nil {
		return nil, errors.New("claudegen: New requires non-nil logger")
	}
	if apiKey == "" {
		return nil, errors.New("claudegen: New requires non-empty apiKey")
	}
	return &ClaudeWorker{
		image: image, model: model, apiKey: apiKey,
		sessionTimeout: timeout, runner: &execContainerRunner{}, log: log,
	}, nil
}

// NewWithRunner 는 ContainerRunner 를 주입하는 생성자입니다 (테스트/DI 용).
func NewWithRunner(image, model, apiKey string, timeout time.Duration, runner ContainerRunner, log *logger.Logger) (*ClaudeWorker, error) {
	if log == nil {
		return nil, errors.New("claudegen: NewWithRunner requires non-nil logger")
	}
	if apiKey == "" {
		return nil, errors.New("claudegen: NewWithRunner requires non-empty apiKey")
	}
	if runner == nil {
		return nil, errors.New("claudegen: NewWithRunner requires non-nil runner")
	}
	return &ClaudeWorker{
		image: image, model: model, apiKey: apiKey,
		sessionTimeout: timeout, runner: runner, log: log,
	}, nil
}

// Start 는 Claude Code 컨테이너를 기동하고 workspace 를 준비합니다.
// 서비스 초기화 시 한 번 호출합니다.
func (w *ClaudeWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.containerID != "" {
		return errors.New("claudegen: worker already started")
	}

	workDir, err := os.MkdirTemp("", "claudegen-workspace-*")
	if err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}

	// ANTHROPIC_API_KEY 는 env 슬라이스로만 전달 — args 포함 시 ps 에 노출됨 (보안).
	env := []string{"ANTHROPIC_API_KEY=" + w.apiKey}
	containerID, err := w.runner.StartContainer(ctx, w.image, workDir, env)
	if err != nil {
		os.RemoveAll(workDir)
		return fmt.Errorf("start claude container: %w", err)
	}

	w.containerID = containerID
	w.workDir = workDir
	w.log.WithFields(map[string]interface{}{
		"container_id": containerID,
		"image":        w.image,
		"model":        w.model,
	}).Info("claude code container started (warm)")
	return nil
}

// Stop 은 진행 중인 Extract 호출 완료를 대기한 뒤 컨테이너를 종료하고 workspace 를 정리합니다.
// graceful shutdown 시 호출합니다. 멱등(이미 정지된 경우 noop).
// StopContainer 실패 시 state 를 소거하지 않아 재시도가 가능합니다.
func (w *ClaudeWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if w.containerID == "" {
		w.mu.Unlock()
		return nil
	}
	// containerID 를 먼저 비워 새 Extract() 호출이 즉시 "not started" 에러를 반환하도록 함.
	containerID := w.containerID
	workDir := w.workDir
	w.containerID = ""
	w.workDir = ""
	w.mu.Unlock()

	// 진행 중인 모든 Extract() 호출 완료 대기 (graceful shutdown).
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		w.log.Warn("stop timeout — forcing container removal with in-flight sessions")
	}

	if err := w.runner.StopContainer(ctx, containerID); err != nil {
		// 실패 시 state 복원 — 다음 Stop() 호출로 재시도 가능.
		w.mu.Lock()
		if w.containerID == "" {
			w.containerID = containerID
			w.workDir = workDir
		}
		w.mu.Unlock()
		return fmt.Errorf("stop claude container: %w", err)
	}
	os.RemoveAll(workDir)
	w.log.Info("claude code container stopped")
	return nil
}

// Extract 는 실행 중인 컨테이너에 새 세션을 생성해 CSS 셀렉터를 추출합니다.
//
// 실행 흐름:
//  1. 세션 고유 디렉토리 생성 (workDir/<sessionID>/)
//  2. page.html 기록
//  3. docker exec <containerID> claude --model ... -p <prompt>
//  4. stdout JSON 파싱 → SelectorMap 반환
//  5. 세션 디렉토리 정리 (defer)
func (w *ClaudeWorker) Extract(ctx context.Context, host string, targetType storage.TargetType, html string) (storage.SelectorMap, error) {
	// wg.Add 를 락 획득보다 먼저 수행 — Stop() 의 wg.Wait() 이 이 Extract 호출을 놓치지 않도록 함.
	w.wg.Add(1)
	defer w.wg.Done()

	w.mu.RLock()
	containerID := w.containerID
	workDir := w.workDir
	w.mu.RUnlock()

	if containerID == "" {
		return storage.SelectorMap{}, errors.New("claudegen: worker not started — call Start() first")
	}

	sessionID, err := newSessionID()
	if err != nil {
		return storage.SelectorMap{}, fmt.Errorf("generate session id: %w", err)
	}

	sessionHostDir := filepath.Join(workDir, sessionID)
	if err := os.MkdirAll(sessionHostDir, 0o755); err != nil {
		return storage.SelectorMap{}, fmt.Errorf("create session dir: %w", err)
	}
	defer os.RemoveAll(sessionHostDir)

	if err := os.WriteFile(filepath.Join(sessionHostDir, "page.html"), []byte(html), 0o644); err != nil {
		return storage.SelectorMap{}, fmt.Errorf("write html: %w", err)
	}

	sessionContainerPath := "/workspace/" + sessionID

	runCtx, cancel := context.WithTimeout(ctx, w.sessionTimeout)
	defer cancel()

	args := []string{
		"claude",
		"--model", w.model,
		"--dangerously-skip-permissions",
		"-p", buildPrompt(host, targetType, sessionContainerPath),
	}

	w.log.WithFields(map[string]interface{}{
		"host":         host,
		"target_type":  string(targetType),
		"container_id": containerID,
		"session_id":   sessionID,
	}).Debug("starting claude code session")

	stdout, stderr, err := w.runner.ExecSession(runCtx, containerID, args)
	if err != nil {
		return storage.SelectorMap{}, fmt.Errorf("claude code exec session: %w (stderr: %s)",
			err, truncate(stderr, truncateStderrLen))
	}

	sm, err := parseSelectorOutput(stdout)
	if err != nil {
		w.log.WithFields(map[string]interface{}{
			"host":        host,
			"target_type": string(targetType),
			"raw_output":  truncate(stdout, truncateStdoutLen),
		}).Debug("claude code session output parse failed")
		return storage.SelectorMap{}, fmt.Errorf("parse claude output: %w", err)
	}

	w.log.WithFields(map[string]interface{}{
		"host":        host,
		"target_type": string(targetType),
	}).Info("claude code session extraction succeeded")

	return sm, nil
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
			if depth == 0 {
				// JSON 시작 전 stray '}' — 무시하고 계속 탐색.
				continue
			}
			depth--
			if depth == 0 && start != -1 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func truncate(s string, n int) string {
	var count int
	for i := range s {
		if count == n {
			return s[:i] + "..."
		}
		count++
	}
	return s
}
