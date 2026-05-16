package claude_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/model"
	"issuetracker/pkg/agent/claude"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

// claudegenLoader 는 claudegen worker 가 요구하는 prompt asset 을 in-memory 로 제공합니다.
// 운영의 pkg/llm/prompt/assets/parser/claude/{page,list}.user.txt 와 동일한 placeholder 사용.
// {{VALIDATION_REJECT_REASON_CONTEXT}} 는 이슈 #365 — reason 부재 시 빈 문자열로 치환.
var claudegenLoader = prompt.MapLoader{
	"parser/claude/page.user": "Read {{SESSION_PATH}}/page.html from {{HOST}} ({{TARGET_TYPE}}). Return JSON.{{VALIDATION_REJECT_REASON_CONTEXT}}",
	"parser/claude/list.user": "Read {{SESSION_PATH}}/page.html from {{HOST}} ({{TARGET_TYPE}}). Return list JSON.{{VALIDATION_REJECT_REASON_CONTEXT}}",
}

// mockContainerRunner 는 docker 를 실행하지 않는 테스트용 ContainerRunner 입니다.
type mockContainerRunner struct {
	startErr   error
	stopErr    error
	execStdout string
	execStderr string
	execErr    error

	mu          sync.Mutex
	startedWith struct {
		image             string
		workDir           string
		authDir           string
		containerAuthPath string
	}
	execedWith struct {
		containerID string
		args        []string
	}
}

func (m *mockContainerRunner) StartContainer(_ context.Context, image, workDir, authDir, containerAuthPath string) (string, error) {
	m.mu.Lock()
	m.startedWith.image = image
	m.startedWith.workDir = workDir
	m.startedWith.authDir = authDir
	m.startedWith.containerAuthPath = containerAuthPath
	m.mu.Unlock()
	if m.startErr != nil {
		return "", m.startErr
	}
	return "mock-container-id", nil
}

func (m *mockContainerRunner) ExecSession(_ context.Context, containerID string, args []string) (string, string, error) {
	m.mu.Lock()
	m.execedWith.containerID = containerID
	m.execedWith.args = args
	m.mu.Unlock()
	return m.execStdout, m.execStderr, m.execErr
}

func (m *mockContainerRunner) StopContainer(_ context.Context, _ string) error {
	return m.stopErr
}

// makeAuthDir 은 테스트용 임시 인증 디렉토리를 생성합니다 (이슈 #266).
func makeAuthDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// 빈 디렉토리이지만 존재 + 디렉토리 검증을 통과하도록 임시 파일 추가
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".credentials"), []byte("mock"), 0o600))
	return dir
}

func newTestWorker(t *testing.T, runner *mockContainerRunner) *claude.Worker {
	t.Helper()
	log := logger.New(logger.DefaultConfig())
	authDir := makeAuthDir(t)
	w, err := claude.NewWithRunner(
		"ghcr.io/anthropics/claude-code:latest",
		"claude-sonnet-4-6",
		authDir,
		"/home/node/.claude",
		10*time.Second,
		runner,
		claudegenLoader,
		log,
	)
	require.NoError(t, err)
	return w
}

// TestClaudeWorker_StartStop 는 컨테이너 기동/종료 흐름을 검증합니다.
func TestClaudeWorker_StartStop(t *testing.T) {
	runner := &mockContainerRunner{}
	w := newTestWorker(t, runner)

	require.NoError(t, w.Start(t.Context()))
	assert.Equal(t, "ghcr.io/anthropics/claude-code:latest", runner.startedWith.image)

	// auth_token 마운트 검증 (이슈 #266) — authDir 과 containerAuthPath 가 StartContainer 에 전달됐는지 확인
	assert.NotEmpty(t, runner.startedWith.authDir, "authDir must be passed to StartContainer")
	assert.Equal(t, "/home/node/.claude", runner.startedWith.containerAuthPath)

	require.NoError(t, w.Stop(t.Context()))
}

// TestClaudeWorker_Start_Idempotent_Fails 는 이미 기동된 워커에 Start 를 다시 호출하면 에러를 반환하는지 검증합니다.
func TestClaudeWorker_Start_Idempotent_Fails(t *testing.T) {
	runner := &mockContainerRunner{}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	err := w.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
	require.NoError(t, w.Stop(t.Context()))
}

// TestClaudeWorker_Stop_BeforeStart_Noop 는 Start 전 Stop 호출이 에러 없이 noop 인지 검증합니다.
func TestClaudeWorker_Stop_BeforeStart_Noop(t *testing.T) {
	runner := &mockContainerRunner{}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Stop(t.Context()))
}

// TestClaudeWorker_Extract_Success_ArticlePage 는 성공적인 셀렉터 추출을 검증합니다.
func TestClaudeWorker_Extract_Success_ArticlePage(t *testing.T) {
	runner := &mockContainerRunner{
		execStdout: `Here are the selectors:
{"title":{"css":"h1.article-title"},"main_content":{"css":"div.article-body","multi":true},"published_at":{"css":"time","attribute":"datetime"}}`,
	}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	sm, err := w.Extract(t.Context(), "news.naver.com", model.TargetTypePage, "<html>...</html>")

	require.NoError(t, err)
	require.NotNil(t, sm.Title)
	assert.Equal(t, "h1.article-title", sm.Title.CSS)
	require.NotNil(t, sm.MainContent)
	assert.True(t, sm.MainContent.Multi)
	require.NotNil(t, sm.PublishedAt)
	assert.Equal(t, "datetime", sm.PublishedAt.Attribute)
}

// TestClaudeWorker_Extract_Success_ListPage 는 리스트 페이지 셀렉터 추출을 검증합니다.
func TestClaudeWorker_Extract_Success_ListPage(t *testing.T) {
	runner := &mockContainerRunner{
		execStdout: `{"item_container":{"css":"ul.news-list li"},"item_link":{"css":"a.news-link","attribute":"href"},"item_title":{"css":"span.title"}}`,
	}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	sm, err := w.Extract(t.Context(), "news.daum.net", model.TargetTypeList, "<html>...</html>")

	require.NoError(t, err)
	require.NotNil(t, sm.ItemContainer)
	assert.Equal(t, "ul.news-list li", sm.ItemContainer.CSS)
	require.NotNil(t, sm.ItemLink)
	assert.Equal(t, "href", sm.ItemLink.Attribute)
}

// TestClaudeWorker_Extract_WithoutStart_Fails 는 Start 전 Extract 호출이 에러를 반환하는지 검증합니다.
func TestClaudeWorker_Extract_WithoutStart_Fails(t *testing.T) {
	runner := &mockContainerRunner{}
	w := newTestWorker(t, runner)

	_, err := w.Extract(t.Context(), "example.com", model.TargetTypePage, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not started")
}

// TestClaudeWorker_Extract_ExecError 는 docker exec 실패 시 에러를 반환하는지 검증합니다.
func TestClaudeWorker_Extract_ExecError(t *testing.T) {
	runner := &mockContainerRunner{
		execStderr: "exec error",
		execErr:    errors.New("exit status 1"),
	}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	_, err := w.Extract(t.Context(), "example.com", model.TargetTypePage, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exec session")
}

// TestClaudeWorker_Extract_NoJSONInOutput 는 JSON 없는 출력 시 파싱 에러를 반환하는지 검증합니다.
func TestClaudeWorker_Extract_NoJSONInOutput(t *testing.T) {
	runner := &mockContainerRunner{
		execStdout: "I could not find any relevant selectors.",
	}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	_, err := w.Extract(t.Context(), "example.com", model.TargetTypePage, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse claude output")
}

// TestClaudeWorker_Extract_ModelInExecArgs 는 --model 인자가 docker exec 에 포함되는지 검증합니다.
func TestClaudeWorker_Extract_ModelInExecArgs(t *testing.T) {
	runner := &mockContainerRunner{execStdout: `{"title":{"css":"h1"}}`}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	_, _ = w.Extract(t.Context(), "example.com", model.TargetTypePage, "<html></html>")

	modelFound := false
	for i, arg := range runner.execedWith.args {
		if arg == "--model" && i+1 < len(runner.execedWith.args) && runner.execedWith.args[i+1] == "claude-sonnet-4-6" {
			modelFound = true
		}
	}
	assert.True(t, modelFound, "--model claude-sonnet-4-6 must be in exec args")
}

// TestClaudeWorker_Extract_SessionPathInArgs 는 세션 경로가 exec 인자 프롬프트에 포함되는지 검증합니다.
func TestClaudeWorker_Extract_SessionPathInArgs(t *testing.T) {
	runner := &mockContainerRunner{execStdout: `{"title":{"css":"h1"}}`}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	_, _ = w.Extract(t.Context(), "example.com", model.TargetTypePage, "<html></html>")

	// -p 인자에 /workspace/<sessionID>/page.html 경로가 포함되어야 함
	found := false
	for i, arg := range runner.execedWith.args {
		if arg == "-p" && i+1 < len(runner.execedWith.args) {
			if len(runner.execedWith.args[i+1]) > 0 {
				// /workspace/ 로 시작하는 경로가 포함되어 있는지만 확인
				prompt := runner.execedWith.args[i+1]
				if len(prompt) > 0 && containsStr(prompt, "/workspace/") && containsStr(prompt, "page.html") {
					found = true
				}
			}
		}
	}
	assert.True(t, found, "prompt must contain /workspace/<sessionID>/page.html")
}

// TestClaudeWorker_Extract_ConcurrentSessions 는 동시 Extract 호출이 안전한지 검증합니다.
func TestClaudeWorker_Extract_ConcurrentSessions(t *testing.T) {
	runner := &mockContainerRunner{execStdout: `{"title":{"css":"h1.article-title"},"main_content":{"css":"article p"}}`}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	const n = 10
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := w.Extract(t.Context(), "example.com", model.TargetTypePage, "<html></html>")
			errs <- err
		}()
	}
	for range n {
		assert.NoError(t, <-errs)
	}
}

// TestClaudeWorker_ModelName 은 ModelName() 이 설정된 모델을 반환하는지 검증합니다.
func TestClaudeWorker_ModelName(t *testing.T) {
	runner := &mockContainerRunner{}
	w := newTestWorker(t, runner)
	assert.Equal(t, "claude-sonnet-4-6", w.ModelName())
}

// ─────────────────────────────────────────────────────────────────────────────
// 인증 디렉토리 검증 테스트 (이슈 #266)
// ─────────────────────────────────────────────────────────────────────────────

// TestNewFromEnv_MissingAuthDir 는 CLAUDE_CODE_AUTH_DIR 미존재 경로 지정 시 에러를 반환하는지 검증합니다.
func TestNewFromEnv_MissingAuthDir(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTH_DIR", "/nonexistent/path/to/claude/auth")
	log := logger.New(logger.DefaultConfig())
	_, err := claude.NewFromEnv(claudegenLoader, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth dir")
}

// TestNewFromEnv_AuthDirIsFile 는 CLAUDE_CODE_AUTH_DIR 가 디렉토리가 아닌 파일을 가리킬 때 에러를 반환하는지 검증합니다.
func TestNewFromEnv_AuthDirIsFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0o644))
	t.Setenv("CLAUDE_CODE_AUTH_DIR", tmpFile)
	log := logger.New(logger.DefaultConfig())
	_, err := claude.NewFromEnv(claudegenLoader, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// TestNewFromEnv_ValidAuthDir 는 유효한 인증 디렉토리로 NewFromEnv 가 성공하는지 검증합니다.
func TestNewFromEnv_ValidAuthDir(t *testing.T) {
	authDir := makeAuthDir(t)
	t.Setenv("CLAUDE_CODE_AUTH_DIR", authDir)
	log := logger.New(logger.DefaultConfig())
	w, err := claude.NewFromEnv(claudegenLoader, log)
	require.NoError(t, err)
	assert.NotNil(t, w)
}

// TestNew_EmptyAuthDir 는 New() 에 빈 authDir 전달 시 에러를 반환하는지 검증합니다.
func TestNew_EmptyAuthDir(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	_, err := claude.New("image", "model", "", "/home/node/.claude", 10*time.Second, claudegenLoader, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authDir")
}

// TestNewWithRunner_EmptyAuthDir 는 NewWithRunner() 에 빈 authDir 전달 시 에러를 반환하는지 검증합니다.
func TestNewWithRunner_EmptyAuthDir(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	runner := &mockContainerRunner{}
	_, err := claude.NewWithRunner("image", "model", "", "/home/node/.claude", 10*time.Second, runner, claudegenLoader, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authDir")
}

// TestNewWithRunner_NilRunner 는 NewWithRunner() 에 nil runner 전달 시 에러를 반환하는지 검증합니다.
func TestNewWithRunner_NilRunner(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	authDir := makeAuthDir(t)
	_, err := claude.NewWithRunner("image", "model", authDir, "/home/node/.claude", 10*time.Second, nil, claudegenLoader, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runner")
}

// TestNewWithRunner_DefaultContainerAuthPath 는 빈 containerAuthPath 전달 시 기본값(/home/node/.claude)이 적용되는지 검증합니다.
func TestNewWithRunner_DefaultContainerAuthPath(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	authDir := makeAuthDir(t)
	runner := &mockContainerRunner{}
	w, err := claude.NewWithRunner("image", "model", authDir, "", 10*time.Second, runner, claudegenLoader, log)
	require.NoError(t, err)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	assert.Equal(t, "/home/node/.claude", runner.startedWith.containerAuthPath, "빈 containerAuthPath → 기본값 적용")
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
