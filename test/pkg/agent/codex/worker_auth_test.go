// 본 파일은 이슈 #537 (authDir 지연 검증) 의 회귀 방지에 한정합니다.
//
// codex 패키지의 포괄적 단위 테스트 (Extract / 세션 lifecycle / 동시성) 는 이슈 #535 에서
// 별도로 다룹니다 — 여기의 mockRunner 도 그때 확장될 것을 전제로 최소 구현만 둡니다.
package codex_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/agent/codex"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

// mockRunner 는 docker 를 실행하지 않는 테스트용 ContainerRunner 입니다.
type mockRunner struct {
	mu           sync.Mutex
	startCalls   int
	startedImage string
}

func (m *mockRunner) StartContainer(_ context.Context, image, _, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startCalls++
	m.startedImage = image
	return "mock-container", nil
}

func (m *mockRunner) ExecSession(_ context.Context, _ string, _ []string) (string, string, error) {
	return "", "", nil
}

func (m *mockRunner) StopContainer(_ context.Context, _ string) error { return nil }

func (m *mockRunner) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startCalls
}

func newWorker(t *testing.T, authDir string, runner codex.ContainerRunner) *codex.Worker {
	t.Helper()
	w, err := codex.NewWithRunner(
		"issuetracker-codex:local", "gpt-5-codex", authDir,
		"/home/node/.codex", 10*time.Second, runner,
		prompt.MapLoader{}, logger.New(logger.DefaultConfig()),
	)
	require.NoError(t, err)
	return w
}

// TestNewWithRunner_AbsentAuthDir_ConstructsOK 는 존재하지 않는 authDir 로도 생성이
// 성공하는지 검증합니다 (이슈 #537) — NewWithRunner 는 DI 진입점이므로 생성 시점에
// 파일시스템을 읽으면 mock 주입 의도와 모순됩니다.
func TestNewWithRunner_AbsentAuthDir_ConstructsOK(t *testing.T) {
	w := newWorker(t, filepath.Join(t.TempDir(), "absent"), &mockRunner{})
	assert.NotNil(t, w)
}

// TestStart_AbsentAuthDir_Fails 는 미존재 authDir 이 Start 시점에 거부되는지,
// 그리고 컨테이너가 기동되지 않는지 검증합니다.
func TestStart_AbsentAuthDir_Fails(t *testing.T) {
	runner := &mockRunner{}
	w := newWorker(t, filepath.Join(t.TempDir(), "absent"), runner)

	err := w.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth dir")
	assert.Zero(t, runner.calls(), "검증 실패 시 컨테이너를 기동하지 않는다")
}

// TestStart_AuthDirIsFile_Fails 는 authDir 이 파일을 가리킬 때 Start 가 거부하는지 검증합니다.
func TestStart_AuthDirIsFile_Fails(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))

	runner := &mockRunner{}
	w := newWorker(t, f, runner)

	err := w.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
	assert.Zero(t, runner.calls())
}

// TestStart_ValidAuthDir_Succeeds 는 유효한 authDir 에서 Start 가 컨테이너를 기동하는지
// 검증합니다 — 지연 검증이 정상 경로를 막지 않음을 확인합니다.
func TestStart_ValidAuthDir_Succeeds(t *testing.T) {
	authDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "auth.json"), []byte("{}"), 0o600))

	runner := &mockRunner{}
	w := newWorker(t, authDir, runner)

	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	assert.Equal(t, 1, runner.calls())
	assert.Equal(t, "issuetracker-codex:local", runner.startedImage)
}

// TestNewWithRunner_EmptyAuthDir 는 빈 authDir 은 여전히 **생성 시점에** 거부되는지
// 검증합니다 — 빈 값은 파일시스템 조회 없이 판단 가능한 프로그래밍 오류입니다.
func TestNewWithRunner_EmptyAuthDir(t *testing.T) {
	_, err := codex.NewWithRunner(
		"image", "model", "", "/home/node/.codex", 10*time.Second,
		&mockRunner{}, prompt.MapLoader{}, logger.New(logger.DefaultConfig()),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authDir")
}
