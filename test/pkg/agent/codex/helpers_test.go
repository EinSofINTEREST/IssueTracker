// 본 파일은 codex 패키지 테스트가 공유하는 mock / 헬퍼를 모읍니다 (이슈 #535).
//
// test/pkg/agent/claude/ 의 구조를 미러링합니다 — 두 패키지가 같은 lifecycle 계약을
// 갖기 때문에 테스트 형태도 대응시켜 두면 한쪽 변경이 다른 쪽에 누락되는 것을 발견하기 쉽습니다.
package codex_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"issuetracker/pkg/agent/codex"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

// codexLoader 는 codex worker 가 요구하는 prompt 를 in-memory 로 제공합니다.
//
// 키는 codex 가 실제로 요청하는 이름 (= claude 와 공용하는 asset) 이어야 한다 — 상수를
// 직접 쓰는 이유다. 리터럴로 적으면 이름이 바뀌었을 때 이 mock 만 조용히 어긋나
// 테스트가 실패 원인을 잘못 가리킨다 (이슈 #594).
//
// **이 loader 로는 이름이 실재하는 asset 을 가리키는지 검증되지 않는다** —
// 그 검증은 prompt_test.go 가 EmbedLoader 로 수행한다.
var codexLoader = prompt.MapLoader{
	codex.PromptNameParserPage: "Read {{SESSION_PATH}}/page.html from {{HOST}} ({{TARGET_TYPE}}). Return JSON.{{VALIDATION_REJECT_REASON_CONTEXT}}",
	codex.PromptNameParserList: "Read {{SESSION_PATH}}/page.html from {{HOST}} ({{TARGET_TYPE}}). Return list JSON.{{VALIDATION_REJECT_REASON_CONTEXT}}",
}

// mockRunner 는 docker 를 실행하지 않는 테스트용 ContainerRunner 입니다.
//
// 동시 호출 (pool round-robin / 병렬 Start) 하에서 쓰이므로 모든 필드 접근은 mu 로 보호합니다.
type mockRunner struct {
	// 주입 — 생성 직후 설정하고 이후 변경하지 않습니다.
	id         string // StartContainer 가 반환할 컨테이너 ID. 빈 값이면 "mock-container"
	startErr   error
	stopErr    error
	execStdout string
	execStderr string
	execErr    error

	// execBlocks: true 면 ExecSession 이 ctx 만료까지 블록합니다 (timeout 검증용).
	execBlocks bool

	// onExec: ExecSession 진입 시 호출되는 훅. 세션 디렉토리가 **아직 살아 있는** 시점이라
	// 디렉토리 내용을 검사할 수 있습니다 (RunSession 이 defer 로 지우기 전).
	onExec func(containerID string, args []string)

	mu         sync.Mutex
	startCalls int
	stopCalls  int
	execCalls  int
	startedAt  struct {
		image             string
		workDir           string
		authDir           string
		containerAuthPath string
	}
	lastExecArgs []string
	stoppedIDs   []string
}

func (m *mockRunner) StartContainer(_ context.Context, image, workDir, authDir, containerAuthPath string) (string, error) {
	m.mu.Lock()
	m.startCalls++
	m.startedAt.image = image
	m.startedAt.workDir = workDir
	m.startedAt.authDir = authDir
	m.startedAt.containerAuthPath = containerAuthPath
	m.mu.Unlock()

	if m.startErr != nil {
		return "", m.startErr
	}
	if m.id != "" {
		return m.id, nil
	}
	return "mock-container", nil
}

func (m *mockRunner) ExecSession(ctx context.Context, containerID string, args []string) (string, string, error) {
	m.mu.Lock()
	m.execCalls++
	m.lastExecArgs = append([]string(nil), args...)
	hook := m.onExec
	m.mu.Unlock()

	if hook != nil {
		hook(containerID, args)
	}
	if m.execBlocks {
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	return m.execStdout, m.execStderr, m.execErr
}

func (m *mockRunner) StopContainer(_ context.Context, containerID string) error {
	m.mu.Lock()
	m.stopCalls++
	m.stoppedIDs = append(m.stoppedIDs, containerID)
	m.mu.Unlock()
	return m.stopErr
}

func (m *mockRunner) counts() (start, exec, stop int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startCalls, m.execCalls, m.stopCalls
}

func (m *mockRunner) execArgs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.lastExecArgs...)
}

func (m *mockRunner) started() (image, workDir, authDir, containerAuthPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startedAt.image, m.startedAt.workDir, m.startedAt.authDir, m.startedAt.containerAuthPath
}

// makeAuthDir 은 읽기 가능한 인증 디렉토리를 만들어 경로를 반환합니다.
// Start 의 verifyAuthDir 이 os.ReadDir 까지 수행하므로 실제 디렉토리가 필요합니다.
func makeAuthDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{}"), 0o600))
	return dir
}

func testLogger() *logger.Logger { return logger.New(logger.DefaultConfig()) }

// newWorker 는 mock runner 를 주입한 Worker 를 만듭니다 (컨테이너 미기동).
func newWorker(t *testing.T, authDir string, runner codex.ContainerRunner) *codex.Worker {
	t.Helper()
	w, err := codex.NewWithRunner(
		"issuetracker-codex:local", "gpt-5-codex", authDir,
		"/home/node/.codex", 10*time.Second, runner, codexLoader, testLogger(),
	)
	require.NoError(t, err)
	return w
}

// shortTimeoutWorker 는 세션 타임아웃이 짧은 Worker 를 만듭니다 (타임아웃 경로 검증용).
func shortTimeoutWorker(t *testing.T, runner codex.ContainerRunner) *codex.Worker {
	t.Helper()
	w, err := codex.NewWithRunner(
		"issuetracker-codex:local", "gpt-5-codex", makeAuthDir(t),
		"/home/node/.codex", 200*time.Millisecond, runner, codexLoader, testLogger(),
	)
	require.NoError(t, err)
	return w
}

// newStartedWorker 는 유효한 authDir + mock runner 로 Worker 를 만들고 Start 까지 마칩니다.
func newStartedWorker(t *testing.T, runner *mockRunner) *codex.Worker {
	t.Helper()
	w := newWorker(t, makeAuthDir(t), runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })
	return w
}

// newPool 은 runners 각각에 대응하는 worker 로 Pool 을 구성합니다 (Start 전).
//
// runner 를 worker 별로 분리하는 이유: round-robin 분배와 부분 기동 실패를 runner 단위
// 호출 횟수로 구별하기 위함입니다.
func newPool(t *testing.T, runners ...*mockRunner) *codex.Pool {
	t.Helper()
	workers := make([]*codex.Worker, 0, len(runners))
	for i, r := range runners {
		if r.id == "" {
			r.id = fmt.Sprintf("mock-container-%d", i)
		}
		workers = append(workers, newWorker(t, makeAuthDir(t), r))
	}
	p, err := codex.NewPool(workers, testLogger())
	require.NoError(t, err)
	return p
}

// okOutput 은 validity=ok 응답 JSON 을 만듭니다.
//
// selectors 는 model.SelectorMap 구조 — 각 필드가 문자열이 아니라 {"css": ...} 객체입니다.
// 문자열로 쓰면 unmarshal 단계에서 실패하므로 실제 prompt 스키마와 형태를 맞춥니다.
func okOutput(titleCSS string) string {
	return fmt.Sprintf(`{"validity":"ok","page_type":"article","page_type_confidence":0.9,`+
		`"article":true,"article_confidence":0.95,`+
		`"selectors":{"title":{"css":%q},"main_content":{"css":"div.body"}}}`, titleCSS)
}
