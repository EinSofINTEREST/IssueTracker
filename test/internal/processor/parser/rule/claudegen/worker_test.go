package claudegen_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/claudegen"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// mockContainerRunner 는 docker 를 실행하지 않는 테스트용 ContainerRunner 입니다.
type mockContainerRunner struct {
	startErr   error
	stopErr    error
	execStdout string
	execStderr string
	execErr    error

	mu          sync.Mutex
	startedWith struct {
		image   string
		workDir string
		env     []string
	}
	execedWith struct {
		containerID string
		args        []string
	}
}

func (m *mockContainerRunner) StartContainer(_ context.Context, image, workDir string, env []string) (string, error) {
	m.mu.Lock()
	m.startedWith.image = image
	m.startedWith.workDir = workDir
	m.startedWith.env = env
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

func newTestWorker(t *testing.T, runner *mockContainerRunner) *claudegen.ClaudeWorker {
	t.Helper()
	log := logger.New(logger.DefaultConfig())
	w, err := claudegen.NewWithRunner(
		"ghcr.io/anthropics/claude-code:latest",
		"claude-sonnet-4-6",
		"test-api-key",
		10*time.Second,
		runner,
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

	// API 키가 env 로 전달됐는지 확인 (ps/proc 노출 방지)
	found := false
	for _, e := range runner.startedWith.env {
		if e == "ANTHROPIC_API_KEY=test-api-key" {
			found = true
		}
	}
	assert.True(t, found, "ANTHROPIC_API_KEY must be in env slice")

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

	sm, err := w.Extract(t.Context(), "news.naver.com", storage.TargetTypePage, "<html>...</html>")

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

	sm, err := w.Extract(t.Context(), "news.daum.net", storage.TargetTypeList, "<html>...</html>")

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

	_, err := w.Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")
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

	_, err := w.Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")
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

	_, err := w.Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse claude output")
}

// TestClaudeWorker_Extract_ModelInExecArgs 는 --model 인자가 docker exec 에 포함되는지 검증합니다.
func TestClaudeWorker_Extract_ModelInExecArgs(t *testing.T) {
	runner := &mockContainerRunner{execStdout: `{"title":{"css":"h1"}}`}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	_, _ = w.Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")

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

	_, _ = w.Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")

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
			_, err := w.Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")
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

// TestNewFromEnv_MissingAPIKey 는 ANTHROPIC_API_KEY 없을 때 에러를 반환하는지 검증합니다.
func TestNewFromEnv_MissingAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	log := logger.New(logger.DefaultConfig())
	_, err := claudegen.NewFromEnv(log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ANTHROPIC_API_KEY")
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
