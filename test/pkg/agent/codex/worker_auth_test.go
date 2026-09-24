// 본 파일은 이슈 #537 (authDir 지연 검증) 의 회귀 방지에 한정합니다.
// 공용 mock / 헬퍼는 helpers_test.go 참조 (이슈 #535).
package codex_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/agent/codex"
)

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

	start, _, _ := runner.counts()
	assert.Zero(t, start, "검증 실패 시 컨테이너를 기동하지 않는다")
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

	start, _, _ := runner.counts()
	assert.Zero(t, start)
}

// TestStart_ValidAuthDir_Succeeds 는 유효한 authDir 에서 Start 가 컨테이너를 기동하는지
// 검증합니다 — 지연 검증이 정상 경로를 막지 않음을 확인합니다.
func TestStart_ValidAuthDir_Succeeds(t *testing.T) {
	runner := &mockRunner{}
	newStartedWorker(t, runner)

	start, _, _ := runner.counts()
	assert.Equal(t, 1, start)

	image, _, authDir, containerAuthPath := runner.started()
	assert.Equal(t, "issuetracker-codex:local", image)
	assert.NotEmpty(t, authDir, "authDir 은 StartContainer 로 전달돼야 한다")
	assert.Equal(t, "/home/node/.codex", containerAuthPath)
}

// TestNewWithRunner_EmptyAuthDir 는 빈 authDir 은 여전히 **생성 시점에** 거부되는지
// 검증합니다 — 빈 값은 파일시스템 조회 없이 판단 가능한 프로그래밍 오류입니다.
func TestNewWithRunner_EmptyAuthDir(t *testing.T) {
	_, err := codex.NewWithRunner(
		"image", "model", "", "/home/node/.codex", 10*time.Second,
		&mockRunner{}, codexLoader, testLogger(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authDir")
}
