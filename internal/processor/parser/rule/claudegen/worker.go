// Package claudegen 은 상시 기동된 Claude Code Docker 컨테이너에 세션을 생성하여
// HTML 에서 CSS 셀렉터를 추출하는 컴포넌트입니다.
//
// 기존 콜드스타트 방식(docker run --rm)과 달리, 컨테이너를 서비스 기동 시 한 번만 띄우고
// (Start) 요청마다 docker exec 으로 새 세션을 생성합니다. 컨테이너 초기화 비용을 최초 1회로
// 상각하여 이후 요청의 레이턴시를 줄입니다.
//
// 인증 방식:
//
//	호스트에서 `claude` CLI 로 사전 로그인하여 발급된 OAuth auth_token 을 사용합니다.
//	Claude 의 인증 상태는 두 위치에 분산되어 있어 둘 다 컨테이너에 마운트합니다:
//	  1. ~/.claude/        : 인증 디렉토리 (.credentials.json, history, 세션 상태 등)
//	  2. ~/.claude.json    : 메인 설정 파일 (sibling — .claude/ 와 같은 부모 디렉토리)
//	두 path 모두 read-write — Claude CLI 가 세션 history 등을 직접 기록하므로 :ro 마운트 불가.
//	구독 quota 안에서 sonnet 호출이 가능합니다. ANTHROPIC_API_KEY 종량제 과금 방식 대체.
//
// 환경변수:
//   - CLAUDE_CODE_AUTH_DIR             : 호스트의 Claude 인증 디렉토리 (기본: $HOME/.claude)
//   - CLAUDE_CODE_CONTAINER_AUTH_PATH  : 컨테이너 내 디렉토리 마운트 경로 (기본: /root/.claude)
//   - CLAUDE_CODE_MODEL                : 모델 ID (기본: claude-sonnet-4-6)
//   - CLAUDE_CODE_IMAGE                : Docker 이미지 (기본: issuetracker-claudegen:local — `make claudegen-build` 필요)
//   - CLAUDE_CODE_TIMEOUT              : 세션 단위 타임아웃 (기본: 120s, Go duration 형식)
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
	// defaultImage 는 deployments/docker/claudegen/Dockerfile 로 빌드한 자체 이미지.
	// Anthropic 공식 ghcr.io/anthropics/claude-code 는 비공개 상태이므로 `make claudegen-build` 로 사전 빌드 필요.
	defaultImage             = "issuetracker-claudegen:local"
	defaultModel             = "claude-sonnet-4-6"
	defaultSessionTimeout    = 120 * time.Second
	defaultContainerAuthPath = "/root/.claude" // 컨테이너 내 인증 마운트 경로

	truncateStderrLen = 512 // exec 실패 시 stderr 미리보기 최대 길이
	truncateStdoutLen = 256 // 파싱 실패 시 stdout 미리보기 최대 길이
)

// ContainerRunner 는 Docker 컨테이너 생명주기를 추상화합니다 (테스트 mock 교체용).
type ContainerRunner interface {
	// StartContainer 는 장기 실행 컨테이너를 기동하고 컨테이너 ID 를 반환합니다.
	//   - workDir: 컨테이너의 /workspace 로 마운트할 호스트 경로 (read-write)
	//   - authDir: 호스트의 Claude 인증 디렉토리 (read-write 마운트 — Claude CLI 가 세션 상태 기록)
	//   - containerAuthPath: 컨테이너 내 authDir 마운트 대상 경로
	StartContainer(ctx context.Context, image, workDir, authDir, containerAuthPath string) (containerID string, err error)

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
	image             string
	model             string
	authDir           string // 호스트 인증 디렉토리
	containerAuthPath string // 컨테이너 내 마운트 경로
	sessionTimeout    time.Duration
	runner            ContainerRunner
	log               *logger.Logger

	mu          sync.RWMutex
	containerID string
	workDir     string
	wg          sync.WaitGroup // 진행 중인 Extract 호출 추적
}

// ModelName 은 이 Worker 가 사용하는 모델 ID 를 반환합니다.
// llmgen.Generator 가 DB description 에 기록할 때 사용합니다.
func (w *ClaudeWorker) ModelName() string { return w.model }

// NewFromEnv 는 환경변수 기반 ClaudeWorker 를 생성합니다.
// Start() 를 호출하기 전까지는 컨테이너가 기동되지 않습니다.
//
// CLAUDE_CODE_AUTH_DIR 미지정 시 $HOME/.claude 를 사용합니다.
// 인증 디렉토리가 없거나 접근 불가하면 fail-fast — 호스트 `claude` CLI 사전 로그인 필요.
func NewFromEnv(log *logger.Logger) (*ClaudeWorker, error) {
	if log == nil {
		return nil, errors.New("claudegen: NewFromEnv requires non-nil logger")
	}
	authDir, err := resolveAuthDir(os.Getenv("CLAUDE_CODE_AUTH_DIR"))
	if err != nil {
		return nil, err
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
		image:             envOr("CLAUDE_CODE_IMAGE", defaultImage),
		model:             envOr("CLAUDE_CODE_MODEL", defaultModel),
		authDir:           authDir,
		containerAuthPath: envOr("CLAUDE_CODE_CONTAINER_AUTH_PATH", defaultContainerAuthPath),
		sessionTimeout:    timeout,
		runner:            &execContainerRunner{},
		log:               log,
	}, nil
}

// New 는 명시적 파라미터로 ClaudeWorker 를 생성합니다 (DI 용).
//
// authDir 은 호스트의 Claude 인증 디렉토리, containerAuthPath 는 컨테이너 내 마운트 대상 경로.
// containerAuthPath 가 빈 문자열이면 defaultContainerAuthPath 사용.
// authDir 은 validateAuthDir 로 절대 경로 정규화 + 존재/디렉토리/읽기 권한 검증.
func New(image, model, authDir, containerAuthPath string, timeout time.Duration, log *logger.Logger) (*ClaudeWorker, error) {
	if log == nil {
		return nil, errors.New("claudegen: New requires non-nil logger")
	}
	resolved, err := validateAuthDir(authDir)
	if err != nil {
		return nil, err
	}
	if containerAuthPath == "" {
		containerAuthPath = defaultContainerAuthPath
	}
	return &ClaudeWorker{
		image: image, model: model,
		authDir: resolved, containerAuthPath: containerAuthPath,
		sessionTimeout: timeout, runner: &execContainerRunner{}, log: log,
	}, nil
}

// NewWithRunner 는 ContainerRunner 를 주입하는 생성자입니다 (테스트/DI 용).
// authDir 은 validateAuthDir 로 절대 경로 정규화 + 존재/디렉토리/읽기 권한 검증.
func NewWithRunner(image, model, authDir, containerAuthPath string, timeout time.Duration, runner ContainerRunner, log *logger.Logger) (*ClaudeWorker, error) {
	if log == nil {
		return nil, errors.New("claudegen: NewWithRunner requires non-nil logger")
	}
	if runner == nil {
		return nil, errors.New("claudegen: NewWithRunner requires non-nil runner")
	}
	resolved, err := validateAuthDir(authDir)
	if err != nil {
		return nil, err
	}
	if containerAuthPath == "" {
		containerAuthPath = defaultContainerAuthPath
	}
	return &ClaudeWorker{
		image: image, model: model,
		authDir: resolved, containerAuthPath: containerAuthPath,
		sessionTimeout: timeout, runner: runner, log: log,
	}, nil
}

// resolveAuthDir 은 환경변수 또는 $HOME 기반으로 인증 디렉토리를 결정하고 접근성을 검증합니다.
// validateAuthDir 로 절대 경로 정규화 + 존재/디렉토리/읽기 권한 검증을 위임합니다.
func resolveAuthDir(envValue string) (string, error) {
	authDir := envValue
	if authDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("claudegen: CLAUDE_CODE_AUTH_DIR not set and cannot determine home dir: %w", err)
		}
		authDir = filepath.Join(home, ".claude")
	}
	return validateAuthDir(authDir)
}

// validateAuthDir 은 인증 디렉토리의 절대 경로를 산출하고 접근성을 검증합니다.
//
//   - 빈 문자열 거부 (호출자가 빈 값 처리 후 호출)
//   - filepath.Abs 로 절대 경로 변환 — Docker 마운트 시 상대 경로 모호성 제거
//   - os.Stat: 존재 + 디렉토리 검증
//   - os.ReadDir: 읽기 권한 검증 (mode 만 보지 않고 실제로 읽어봄)
func validateAuthDir(authDir string) (string, error) {
	if authDir == "" {
		return "", errors.New("claudegen: authDir must not be empty")
	}
	absPath, err := filepath.Abs(authDir)
	if err != nil {
		return "", fmt.Errorf("claudegen: failed to resolve absolute path for %q: %w", authDir, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("claudegen: auth dir %q not accessible: %w (run `claude` CLI on host to login first)", absPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("claudegen: auth dir %q is not a directory", absPath)
	}
	if _, err := os.ReadDir(absPath); err != nil {
		return "", fmt.Errorf("claudegen: auth dir %q is not readable: %w", absPath, err)
	}
	return absPath, nil
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

	containerID, err := w.runner.StartContainer(ctx, w.image, workDir, w.authDir, w.containerAuthPath)
	if err != nil {
		os.RemoveAll(workDir)
		return fmt.Errorf("start claude container: %w", err)
	}

	w.containerID = containerID
	w.workDir = workDir
	// INFO 로그는 운영자가 외부 수집 시스템에서 보는 항목 — 호스트 사용자명/홈 구조 노출 회피.
	// auth_dir 같은 절대 경로는 DEBUG 레벨로 격리.
	w.log.WithFields(map[string]interface{}{
		"container_id": containerID,
		"image":        w.image,
		"model":        w.model,
	}).Info("claude code container started (warm, subscription auth)")
	w.log.WithFields(map[string]interface{}{
		"auth_dir":            w.authDir,
		"container_auth_path": w.containerAuthPath,
	}).Debug("claude code auth mount paths")
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
		// --dangerously-skip-permissions 는 root user 환경에서 동작하지 않고, OAuth 인증 + -p 모드는
		// 본 플래그 없이 정상 동작 — 라이브 검증 시 발견.
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
