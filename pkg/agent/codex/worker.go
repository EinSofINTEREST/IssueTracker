// Package codex 는 상시 기동된 Codex CLI Docker 컨테이너에 세션을 생성하여
// LLM 호출을 수행하는 컴포넌트입니다 (이슈 #462).
//
// 기존 콜드스타트 방식(docker run --rm)과 달리, 컨테이너를 서비스 기동 시 한 번만 띄우고
// (Start) 요청마다 docker exec 으로 새 세션을 생성합니다. 컨테이너 초기화 비용을 최초 1회로
// 상각하여 이후 요청의 레이턴시를 줄입니다.
//
// 인증 방식:
//
//	호스트에서 `codex` CLI 로 사전 로그인하여 발급된 인증 상태 (ChatGPT 구독 OAuth 또는
//	OPENAI_API_KEY) 를 사용합니다. 인증 정보는 ~/.codex/ 디렉토리에 보관되며 컨테이너에
//	read-write 마운트됩니다 — Codex CLI 가 세션 상태 등을 본 디렉토리에 기록하므로 :ro 불가.
//
//	claude 와 차이: claude 는 ~/.claude.json (sibling) 도 마운트하지만 codex 는 ~/.codex
//	디렉토리만으로 충분합니다.
//
// 환경변수:
//   - CODEX_AUTH_DIR             : 호스트의 Codex 인증 디렉토리 (기본: $HOME/.codex)
//   - CODEX_CONTAINER_AUTH_PATH  : 컨테이너 내 디렉토리 마운트 경로 (기본: /home/node/.codex)
//   - CODEX_MODEL                : 모델 ID (기본: gpt-5-codex)
//   - CODEX_IMAGE                : Docker 이미지 (기본: issuetracker-codex:local — `make codex-build` 필요)
//   - CODEX_TIMEOUT              : 세션 단위 타임아웃 (기본: 120s, Go duration 형식)
package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/agent"
	agentdb "issuetracker/pkg/agent/dependency/db"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

const (
	// defaultImage 는 deployments/docker/codex/Dockerfile 로 빌드한 자체 이미지.
	// node:22 + npm install -g @openai/codex 패턴 (Sub 2 — 이슈 #533). `make codex-build` 로 사전 빌드 필요.
	defaultImage             = "issuetracker-codex:local"
	defaultModel             = "gpt-5-codex"
	defaultSessionTimeout    = 120 * time.Second
	defaultContainerAuthPath = "/home/node/.codex" // 컨테이너 내 인증 마운트 경로 (이슈 #474 — node user HOME)

	truncateStderrLen = 512 // exec 실패 시 stderr 미리보기 최대 길이
	truncateStdoutLen = 256 // 파싱 실패 시 stdout 미리보기 최대 길이
)

// ContainerRunner 는 Docker 컨테이너 생명주기를 추상화합니다 (테스트 mock 교체용).
type ContainerRunner interface {
	// StartContainer 는 장기 실행 컨테이너를 기동하고 컨테이너 ID 를 반환합니다.
	//   - workDir: 컨테이너의 /workspace 로 마운트할 호스트 경로 (read-write)
	//   - authDir: 호스트의 Codex 인증 디렉토리 (read-write 마운트 — Codex CLI 가 세션 상태 기록)
	//   - containerAuthPath: 컨테이너 내 authDir 마운트 대상 경로
	StartContainer(ctx context.Context, image, workDir, authDir, containerAuthPath string) (containerID string, err error)

	// ExecSession 은 실행 중인 컨테이너에서 명령을 실행합니다.
	ExecSession(ctx context.Context, containerID string, args []string) (stdout, stderr string, err error)

	// StopContainer 는 컨테이너를 강제 종료 + 삭제합니다.
	StopContainer(ctx context.Context, containerID string) error
}

// Worker 는 상시 기동된 Codex 컨테이너에 세션을 생성해 CSS 셀렉터를 추출합니다.
//
// Lifecycle:
//   - Start(ctx): 컨테이너 기동 + workspace 디렉토리 생성
//   - Extract(ctx, ...): 세션별 고유 서브디렉토리 생성 → docker exec → JSON 파싱
//   - Stop(ctx): 컨테이너 종료 + workspace 정리
//
// Extract 는 동시 호출에 안전합니다 — 각 세션이 고유 디렉토리를 사용합니다.
// Stop 은 진행 중인 모든 Extract 완료를 대기합니다 (graceful shutdown).
type Worker struct {
	image             string
	model             string
	authDir           string // 호스트 인증 디렉토리
	containerAuthPath string // 컨테이너 내 마운트 경로
	sessionTimeout    time.Duration
	runner            ContainerRunner
	loader            prompt.Loader
	log               *logger.Logger

	mu          sync.RWMutex
	containerID string
	workDir     string
	wg          sync.WaitGroup // 진행 중인 세션 호출 추적

	// stopping 은 Stop 이 시작됐음을 나타냅니다 (CodeRabbit 피드백).
	//
	// 이 플래그가 없으면 Stop 의 wg.Wait() 이 시작된 뒤에도 새 호출이 wg.Add 를 할 수 있어
	// "WaitGroup is reused before previous Wait has returned" panic 또는 Wait 조기 반환이
	// 발생합니다. admit() 이 같은 mutex 아래에서 이 플래그를 확인하고 Add 까지 마칩니다.
	stopping bool

	// mcpConfig 가 non-nil 이면 RunSession 이 `-c mcp_servers.<name>...` override 로
	// 전달합니다 (이슈 #585). claude 와 달리 .mcp.json 파일을 쓰지 않는다 — codex 의 exec
	// 파서에 파일 주입 옵션이 없기 때문이며, 근거는 mcp.go 참조. nil 이면 비활성.
	//
	// ExtractEnriched (parser selector 추출) 에는 영향 없음 — enrich 경로 전용이며,
	// parser 는 DB 접근이 필요 없어 least-privilege 로 MCP 를 붙이지 않는다.
	mcpConfig *agentdb.MCPConfig
}

// WithMCPConfig 는 RunSession 호출 시 mount 할 MCP 설정을 등록합니다 (이슈 #472).
//
// 본 메소드는 fluent 패턴 — 생성자 N개 + WithMCPConfig 으로 옵션성 보장.
// nil 전달 시 비활성 (기존 nil 상태와 동일). 본 함수는 thread-unsafe — Start() 전에 1회만 호출.
func (w *Worker) WithMCPConfig(c *agentdb.MCPConfig) *Worker {
	w.mcpConfig = c
	return w
}

// ModelName 은 이 Worker 가 사용하는 모델 ID 를 반환합니다.
// llmgen.Generator 가 DB description 에 기록할 때 사용합니다.
func (w *Worker) ModelName() string { return w.model }

// NewFromEnv 는 환경변수 기반 Worker 를 생성합니다 (단일 풀 호환).
// Start() 를 호출하기 전까지는 컨테이너가 기동되지 않습니다.
//
// CODEX_AUTH_DIR 미지정 시 $HOME/.codex 를 사용합니다.
// 인증 디렉토리가 없거나 접근 불가하면 **Start 시점에** 실패합니다 (이슈 #537) —
// 호스트 `codex` CLI 사전 로그인 필요.
//
// 이슈 #530 — stage 별 풀이 필요한 경우 NewPoolFromConfig 가 본 함수가 아닌
// newWorkerFromStageEnv 를 호출하여 stage prefix env 도 lookup 합니다.
func NewFromEnv(loader prompt.Loader, log *logger.Logger) (*Worker, error) {
	if log == nil {
		return nil, errors.New("codex: NewFromEnv requires non-nil logger")
	}
	if loader == nil {
		return nil, errors.New("codex: NewFromEnv requires non-nil prompt loader")
	}
	return newWorkerFromStageEnv(agent.StageEnv{}, loader, log)
}

// newWorkerFromStageEnv 는 stage prefix 인지 env 해석으로 Worker 를 생성합니다 (이슈 #530).
//
// envR.Name() 이 빈 문자열이면 NewFromEnv 와 동일 동작 — `CODEX_*` 만 lookup.
// 명시 시 `<NAME>_CODEX_*` 가 우선 + 미설정 시 base fallback.
func newWorkerFromStageEnv(envR agent.StageEnv, loader prompt.Loader, log *logger.Logger) (*Worker, error) {
	authDir, err := resolveAuthDir(envR.GetOr("CODEX_AUTH_DIR", ""))
	if err != nil {
		return nil, err
	}
	timeout := defaultSessionTimeout
	if s, key := envR.Get("CODEX_TIMEOUT"); s != "" {
		d, perr := time.ParseDuration(s)
		if perr != nil {
			log.WithFields(map[string]interface{}{"env": key, "value": s}).WithError(perr).
				Warn("CODEX_TIMEOUT parse failed, using default")
		} else if d <= 0 {
			log.WithFields(map[string]interface{}{"env": key, "value": s}).
				Warn("CODEX_TIMEOUT must be positive, using default")
		} else {
			timeout = d
		}
	}
	return &Worker{
		image:             envR.GetOr("CODEX_IMAGE", defaultImage),
		model:             envR.GetOr("CODEX_MODEL", defaultModel),
		authDir:           authDir,
		containerAuthPath: envR.GetOr("CODEX_CONTAINER_AUTH_PATH", defaultContainerAuthPath),
		sessionTimeout:    timeout,
		runner:            &execContainerRunner{},
		loader:            loader,
		log:               log,
	}, nil
}

// New 는 명시적 파라미터로 Worker 를 생성합니다 (DI 용).
//
// authDir 은 호스트의 Codex 인증 디렉토리, containerAuthPath 는 컨테이너 내 마운트 대상 경로.
// containerAuthPath 가 빈 문자열이면 defaultContainerAuthPath 사용.
// authDir 은 절대 경로로 정규화만 하며, 존재/권한 검증은 Start 시점입니다 (이슈 #537).
func New(image, model, authDir, containerAuthPath string, timeout time.Duration, loader prompt.Loader, log *logger.Logger) (*Worker, error) {
	if log == nil {
		return nil, errors.New("codex: New requires non-nil logger")
	}
	if loader == nil {
		return nil, errors.New("codex: New requires non-nil prompt loader")
	}
	resolved, err := resolveAuthPath(authDir)
	if err != nil {
		return nil, err
	}
	if containerAuthPath == "" {
		containerAuthPath = defaultContainerAuthPath
	}
	return &Worker{
		image: image, model: model,
		authDir: resolved, containerAuthPath: containerAuthPath,
		sessionTimeout: timeout, runner: &execContainerRunner{}, loader: loader, log: log,
	}, nil
}

// NewWithRunner 는 ContainerRunner 를 주입하는 생성자입니다 (테스트/DI 용).
// authDir 은 절대 경로로 정규화만 하며, 존재/권한 검증은 Start 시점입니다 (이슈 #537).
func NewWithRunner(image, model, authDir, containerAuthPath string, timeout time.Duration, runner ContainerRunner, loader prompt.Loader, log *logger.Logger) (*Worker, error) {
	if log == nil {
		return nil, errors.New("codex: NewWithRunner requires non-nil logger")
	}
	if runner == nil {
		return nil, errors.New("codex: NewWithRunner requires non-nil runner")
	}
	if loader == nil {
		return nil, errors.New("codex: NewWithRunner requires non-nil prompt loader")
	}
	resolved, err := resolveAuthPath(authDir)
	if err != nil {
		return nil, err
	}
	if containerAuthPath == "" {
		containerAuthPath = defaultContainerAuthPath
	}
	return &Worker{
		image: image, model: model,
		authDir: resolved, containerAuthPath: containerAuthPath,
		sessionTimeout: timeout, runner: runner, loader: loader, log: log,
	}, nil
}

// resolveAuthDir 은 환경변수 또는 $HOME 기반으로 인증 디렉토리 경로를 결정합니다.
// 절대 경로 정규화까지만 수행하며, 접근성 검증은 Start 가 담당합니다 (이슈 #537).
func resolveAuthDir(envValue string) (string, error) {
	authDir := envValue
	if authDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("codex: CODEX_AUTH_DIR not set and cannot determine home dir: %w", err)
		}
		authDir = filepath.Join(home, ".codex")
	}
	return resolveAuthPath(authDir)
}

// resolveAuthPath 는 인증 디렉토리의 절대 경로를 산출합니다 (이슈 #537).
//
//   - 빈 문자열 거부 (호출자가 빈 값 처리 후 호출)
//   - filepath.Abs 로 절대 경로 변환 — Docker 마운트 시 상대 경로 모호성 제거
//
// **대상 디렉토리를 읽지 않습니다.** 존재 / 권한 검증은 Start 시점의 verifyAuthDir 담당 —
// 생성자가 파일시스템 부작용을 갖지 않도록 경로 계산과 접근성 검증을 분리했습니다.
func resolveAuthPath(authDir string) (string, error) {
	if authDir == "" {
		return "", errors.New("codex: authDir must not be empty")
	}
	absPath, err := filepath.Abs(authDir)
	if err != nil {
		return "", fmt.Errorf("codex: failed to resolve absolute path for %q: %w", authDir, err)
	}
	return absPath, nil
}

// verifyAuthDir 은 인증 디렉토리의 접근성을 검증합니다 — Start 에서만 호출 (이슈 #537).
//
//   - os.Stat: 존재 + 디렉토리 검증
//   - os.ReadDir: 읽기 권한 검증 (mode 만 보지 않고 실제로 읽어봄)
//
// 생성자가 아닌 Start 에 둔 이유: 생성자가 순수 데이터 조립이어야 테스트가 실제 디렉토리
// 없이 Worker 를 만들 수 있고, 특히 NewWithRunner 의 DI 의도와 모순되지 않습니다. 운영
// 동작은 그대로다 — 호출처 (main.go startClaudegenPool) 가 생성 실패와 Start 실패를 같은
// graceful fallback 으로 처리하므로, 잘못된 authDir 의 결과는 이전과 동일합니다.
func verifyAuthDir(absPath string) error {
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("codex: auth dir %q not accessible: %w (run `codex` CLI on host to login first)", absPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("codex: auth dir %q is not a directory", absPath)
	}
	if _, err := os.ReadDir(absPath); err != nil {
		return fmt.Errorf("codex: auth dir %q is not readable: %w", absPath, err)
	}
	return nil
}

// Start 는 Codex 컨테이너를 기동하고 workspace 를 준비합니다.
// 서비스 초기화 시 한 번 호출합니다.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.containerID != "" {
		return errors.New("codex: worker already started")
	}

	// 인증 디렉토리 접근성은 생성자가 아니라 여기서 검증한다 (이슈 #537) — 생성자는 순수
	// 데이터 조립으로 두고, 파일시스템 부작용은 lifecycle 시작점인 Start 로 모은다.
	if err := verifyAuthDir(w.authDir); err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "codex-workspace-*")
	if err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}
	// 이슈 #474 — 컨테이너 user 가 non-root (node, uid 1000) 이므로 호스트 프로세스가
	// root 인 경우 MkdirTemp 의 기본 권한 0700 으로 컨테이너에서 traverse 불가.
	// 0755 로 완화하여 컨테이너 node user 가 mount 후 read + traverse 가능하도록 함.
	if err := os.Chmod(workDir, 0o755); err != nil {
		os.RemoveAll(workDir)
		return fmt.Errorf("chmod workspace dir: %w", err)
	}

	containerID, err := w.runner.StartContainer(ctx, w.image, workDir, w.authDir, w.containerAuthPath)
	if err != nil {
		os.RemoveAll(workDir)
		return fmt.Errorf("start codex container: %w", err)
	}

	w.containerID = containerID
	w.workDir = workDir
	// INFO 로그는 운영자가 외부 수집 시스템에서 보는 항목 — 호스트 사용자명/홈 구조 노출 회피.
	// auth_dir 같은 절대 경로는 DEBUG 레벨로 격리.
	w.log.WithFields(map[string]interface{}{
		"container_id": containerID,
		"image":        w.image,
		"model":        w.model,
	}).Info("codex container started (warm, subscription auth)")
	w.log.WithFields(map[string]interface{}{
		"auth_dir":            w.authDir,
		"container_auth_path": w.containerAuthPath,
	}).Debug("codex auth mount paths")
	return nil
}

// errWorkerNotStarted 는 Start 전이거나 Stop 이 진행 중일 때 반환됩니다.
var errWorkerNotStarted = errors.New("codex: worker not started — call Start() first")

// admit 은 새 세션 호출을 받아들일지 결정하고, 받아들이면 **같은 lock 안에서** wg.Add(1) 합니다.
//
// 상태 확인과 Add 를 한 임계구역에 두는 것이 핵심입니다 (CodeRabbit 피드백). 나누면
// Stop 이 wg.Wait() 을 시작한 뒤 Add 가 끼어들어 WaitGroup 계약을 위반합니다.
//
// 호출자는 성공 시 반드시 defer w.wg.Done() 을 등록해야 합니다.
func (w *Worker) admit() (containerID, workDir string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopping || w.containerID == "" {
		return "", "", errWorkerNotStarted
	}
	w.wg.Add(1)
	return w.containerID, w.workDir, nil
}

// Stop 은 진행 중인 Extract 호출 완료를 대기한 뒤 컨테이너를 종료하고 workspace 를 정리합니다.
// graceful shutdown 시 호출합니다. 멱등(이미 정지된 경우 noop).
// StopContainer 실패 시 state 를 소거하지 않아 재시도가 가능합니다.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if w.containerID == "" {
		w.mu.Unlock()
		return nil
	}
	// stopping 을 먼저 세워 새 호출의 admit() 이 즉시 거부되게 한다 — 이 lock 을 놓은 뒤에는
	// 어떤 wg.Add 도 일어나지 않으므로 아래 wg.Wait() 이 안전하다.
	w.stopping = true
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

	// gemini #3321915451 — ctx 가 이미 cancel 된 경우 StopContainer 의 exec.CommandContext 가
	// 즉시 실패 → docker rm 미실행 → 컨테이너 누수. 별도 stopCtx 로 cleanup 보장.
	stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer stopCancel()

	if err := w.runner.StopContainer(stopCtx, containerID); err != nil {
		// 실패 시 state 복원 — 다음 Stop() 호출로 재시도 가능.
		// stopping 도 함께 되돌린다. 남겨 두면 컨테이너가 살아 있는데도 admit() 이 영구
		// 거부해 worker 가 좀비가 된다.
		w.mu.Lock()
		if w.containerID == "" {
			w.containerID = containerID
			w.workDir = workDir
		}
		w.stopping = false
		w.mu.Unlock()
		return fmt.Errorf("stop codex container: %w", err)
	}
	os.RemoveAll(workDir)
	w.log.Info("codex container stopped")
	return nil
}

// Extract 는 실행 중인 컨테이너에 새 세션을 생성해 CSS 셀렉터를 추출합니다.
//
// 본 메소드는 SelectorExtractor 인터페이스 호환성을 위한 thin wrapper —
// 내부적으로 ExtractEnriched 를 호출하고 Selectors 만 반환합니다.
// Blacklist 결정은 무시 (호출자가 ExtractEnriched 직접 호출하도록 권장).
//
// llmgen.Generator 는 EnrichedExtractor 인터페이스 type assertion 으로 자동 분기 —
// Worker 는 두 인터페이스 모두 구현.
func (w *Worker) Extract(ctx context.Context, host string, targetType model.TargetType, html string) (model.SelectorMap, error) {
	res, err := w.ExtractEnriched(ctx, host, targetType, html)
	if err != nil {
		return model.SelectorMap{}, err
	}
	if res.Blacklist != nil {
		return model.SelectorMap{}, fmt.Errorf("page blacklisted: %s", res.Blacklist.Reason)
	}
	return res.Selectors, nil
}

// ExtractEnriched 는 multi-step 추출을 수행합니다 — 페이지 유효성 / 타입 / 셀렉터 + 자가 검증.
//
// 실행 흐름:
//  1. 세션 고유 디렉토리 생성 (workDir/<sessionID>/)
//  2. page.html 기록
//  3. docker exec <containerID> codex --model ... -p <prompt>
//  4. stdout JSON 파싱 → ExtractResult (validity / page_type / selectors / self_check)
//  5. 세션 디렉토리 정리 (defer)
//
// validity == "blacklist" 면 ExtractResult.Blacklist 비-nil — 호출자가 셀렉터 INSERT skip
// + parser_blacklist Upsert 분기. Selectors / PageType 은 의미 없음.
func (w *Worker) ExtractEnriched(ctx context.Context, host string, targetType model.TargetType, html string) (*llmgen.ExtractResult, error) {
	containerID, workDir, err := w.admit()
	if err != nil {
		return nil, err
	}
	defer w.wg.Done()

	sessionID, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}

	sessionHostDir := filepath.Join(workDir, sessionID)
	if err := os.MkdirAll(sessionHostDir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	defer os.RemoveAll(sessionHostDir)

	if err := os.WriteFile(filepath.Join(sessionHostDir, "page.html"), []byte(html), 0o644); err != nil {
		return nil, fmt.Errorf("write html: %w", err)
	}

	sessionContainerPath := "/workspace/" + sessionID

	runCtx, cancel := context.WithTimeout(ctx, w.sessionTimeout)
	defer cancel()

	// validator → parser 재학습 cycle (이슈 #365): ctx 에서 reject reason 추출 →
	// prompt 에 컨텍스트 블록으로 주입. 부재 시 빈 문자열 → placeholder 영향 없음.
	rejectReason, _ := llmgen.RejectReasonFromContext(ctx)
	promptText, err := buildPrompt(w.loader, host, targetType, sessionContainerPath, rejectReason)
	if err != nil {
		return nil, fmt.Errorf("build codex prompt: %w", err)
	}
	// Codex CLI 비대화 모드 — `codex exec` 가 표준 (claude 와 차이).
	// 권한/sandbox 플래그는 codex CLI 버전 진화 빈도가 높아 본 PR 골격에서는 지정 안 함 —
	// 운영자가 필요 시 CODEX_EXTRA_ARGS (후속 이슈) 또는 직접 args 확장으로 추가.
	args := []string{
		"codex", "exec",
		// --skip-git-repo-check: codex 는 기본적으로 git 작업 트리 안에서만 실행을 허용한다.
		// 컨테이너 WORKDIR (/workspace) 은 git repo 가 아니므로 (이미지에 git 도 없다) 이
		// 플래그가 없으면 프롬프트 실행 전에 거부된다 — 이슈 #591.
		"--skip-git-repo-check",
		"--model", w.model,
		promptText,
	}

	w.log.WithFields(map[string]interface{}{
		"host":         host,
		"target_type":  string(targetType),
		"container_id": containerID,
		"session_id":   sessionID,
	}).Debug("starting codex session")

	stdout, stderr, err := w.runner.ExecSession(runCtx, containerID, args)
	if err != nil {
		return nil, fmt.Errorf("codex exec session: %w (stderr: %s)",
			err, truncate(stderr, truncateStderrLen))
	}

	res, err := parseEnrichedOutput(stdout)
	if err != nil {
		w.log.WithFields(map[string]interface{}{
			"host":        host,
			"target_type": string(targetType),
			"raw_output":  truncate(stdout, truncateStdoutLen),
		}).Debug("codex session output parse failed")
		return nil, fmt.Errorf("parse codex output: %w", err)
	}

	logFields := map[string]interface{}{
		"host":        host,
		"target_type": string(targetType),
	}
	if res.Blacklist != nil {
		logFields["blacklist_reason"] = res.Blacklist.Reason
		w.log.WithFields(logFields).Info("codex session marked page as blacklist")
	} else {
		logFields["page_type"] = string(res.PageType)
		logFields["page_type_confidence"] = res.PageTypeConfidence
		w.log.WithFields(logFields).Info("codex session extraction succeeded")
	}

	return res, nil
}

// enrichedOutput 은 새 prompt schema 의 응답 구조 (codex multi-step extraction).
//
// validity == "blacklist" 일 때 selectors / self_check 는 비어있을 수 있음 — Generator 의
// 호출자가 Blacklist 분기로 즉시 진입하므로 의미 없음.
type enrichedOutput struct {
	Validity           string            `json:"validity"`
	BlacklistReason    string            `json:"blacklist_reason"`
	BlacklistMode      string            `json:"blacklist_mode"` // 이슈 #480 — "drop" | "extract_links_only" (legacy: 빈 문자열 → drop fallback)
	PageType           string            `json:"page_type"`
	PageTypeConfidence float64           `json:"page_type_confidence"`
	Article            bool              `json:"article"`
	ArticleConfidence  float64           `json:"article_confidence"`
	Selectors          model.SelectorMap `json:"selectors"`
	// SelfCheck 는 운영 진단용 — 본 worker 는 currently 무시 (호출자가 metrics / 로그에 활용 가능).
	// 향후 self_check.warnings 가 있으면 LLM 재시도 trigger 로 사용 가능.
	SelfCheck json.RawMessage `json:"self_check,omitempty"`
}

// parseEnrichedOutput 은 새 schema 응답을 ExtractResult 로 변환합니다.
//
// validity == "blacklist" → ExtractResult.Blacklist 비-nil, 다른 필드는 의미 없음.
// validity == "ok" → Selectors / PageType 정상 채움. Blacklist == nil.
// validity 가 빈 문자열 / 미인식 값 → legacy SelectorMap-only output 으로 간주하고 재파싱.
func parseEnrichedOutput(output string) (*llmgen.ExtractResult, error) {
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON object found in output")
	}

	var eo enrichedOutput
	if err := json.Unmarshal([]byte(jsonStr), &eo); err != nil {
		return nil, fmt.Errorf("unmarshal enriched output: %w", err)
	}

	switch eo.Validity {
	case "blacklist":
		reason := strings.TrimSpace(eo.BlacklistReason)
		if reason == "" {
			reason = "(no reason provided)"
		}
		// 이슈 #480 — mode 가 명시된 경우 ExtractLinksOnly / Drop 분기. 빈 값 또는 unknown 은
		// 서비스 단에서 default drop 으로 보정되므로 본 단계는 단순 통과.
		// ToLower 정규화 — LLM 응답 case 변종 (DROP / Drop) 이 unknown 으로 downgrade 되어
		// link harvest fallback 이 의도치 않게 막히는 케이스 회피 (coderabbit PR #481 피드백).
		mode := model.BlacklistMode(strings.ToLower(strings.TrimSpace(eo.BlacklistMode)))
		return &llmgen.ExtractResult{
			Blacklist: &llmgen.BlacklistDecision{Reason: reason, Mode: mode},
		}, nil
	case "ok":
		return &llmgen.ExtractResult{
			Selectors:          eo.Selectors,
			PageType:           normalizePageType(eo.PageType),
			PageTypeConfidence: eo.PageTypeConfidence,
			Article:            eo.Article,
			ArticleConfidence:  eo.ArticleConfidence,
		}, nil
	case "":
		// Schema 가 채워지지 않았거나 LLM 이 여전히 legacy SelectorMap-only 를 반환한 경우.
		// 같은 JSON 을 SelectorMap 으로 재파싱 시도 — backward compat fallback.
		//
		// DisallowUnknownFields 를 쓰는 이유 (CodeRabbit 피드백):
		// encoding/json 은 기본적으로 모르는 필드를 **조용히 버립니다.** 따라서
		// `{"selectors": {...}}` 처럼 한 겹 감싸인 응답도 빈 SelectorMap 으로 파싱돼
		// "성공" 으로 돌아갑니다. Generator 경로는 이후 validateSelectors 가 빈 맵을 거부해
		// 잘못된 룰이 저장되지는 않지만, fallback 자체와 직접 호출자는 잘못된 응답을
		// 성공으로 처리하게 됩니다.
		dec := json.NewDecoder(strings.NewReader(jsonStr))
		dec.DisallowUnknownFields()
		var sm model.SelectorMap
		if err := dec.Decode(&sm); err != nil {
			return nil, fmt.Errorf("validity field missing and not a legacy selector map: %w", err)
		}
		return &llmgen.ExtractResult{Selectors: sm}, nil
	default:
		return nil, fmt.Errorf("unrecognized validity %q (expected ok|blacklist)", eo.Validity)
	}
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

// knownPageTypes 는 prompt schema 에 명시된 valid page_type 집합입니다 (CodeRabbit Major 반영).
//
// LLM 응답이 prompt 명세를 벗어나면 (예: "News" 대문자, "news_article" 등 변종) silent 하게
// parser_rules.page_type 에 들어가 분류 통계가 오염될 수 있어 정규화 + 허용 set 검증.
//
// 동의어 매핑: 잘 알려진 변종은 표준 PageType 으로 매핑. 모르는 값은 빈 문자열 (Unspecified)
// 로 fall-back — rule INSERT 자체는 막지 않음 (selector 가 정상이면 분류만 미상태로 저장).
var knownPageTypes = map[string]llmgen.PageType{
	"news":         llmgen.PageTypeNews,
	"news_article": llmgen.PageTypeNews,
	"article":      llmgen.PageTypeNews,
	"community":    llmgen.PageTypeCommunity,
	"forum":        llmgen.PageTypeCommunity,
	"info":         llmgen.PageTypeInfo,
	"information":  llmgen.PageTypeInfo,
	"wiki":         llmgen.PageTypeInfo,
	"commercial":   llmgen.PageTypeCommercial,
	"shopping":     llmgen.PageTypeCommercial,
	"product":      llmgen.PageTypeCommercial,
	"paper":        llmgen.PageTypePaper,
	"academic":     llmgen.PageTypePaper,
	"research":     llmgen.PageTypePaper,
	"other":        llmgen.PageTypeOther,
}

// normalizePageType 은 LLM 응답의 page_type 문자열을 prompt schema 의 valid set 으로
// 정규화합니다. 동의어 / 대소문자 변종 흡수, 미인식 값은 PageTypeUnspecified.
func normalizePageType(raw string) llmgen.PageType {
	key := strings.ToLower(strings.TrimSpace(raw))
	if pt, ok := knownPageTypes[key]; ok {
		return pt
	}
	return llmgen.PageTypeUnspecified
}
