// 본 파일은 mock ContainerRunner 로 Worker 의 컨테이너 lifecycle 을 검증합니다 (이슈 #535).
// 실제 docker 는 실행하지 않습니다.
package codex_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/model"
)

// TestWorker_StartStop 은 기동 → 종료 정상 흐름을 검증합니다.
func TestWorker_StartStop(t *testing.T) {
	runner := &mockRunner{id: "cid-1"}
	w := newWorker(t, makeAuthDir(t), runner)

	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Stop(t.Context()))

	start, _, stop := runner.counts()
	assert.Equal(t, 1, start)
	assert.Equal(t, 1, stop)
	assert.Equal(t, []string{"cid-1"}, runner.stoppedIDs, "Start 가 돌려준 ID 로 종료해야 한다")
}

// TestWorker_Start_Twice_Fails 는 이미 기동된 worker 의 재기동이 거부되는지 검증합니다.
func TestWorker_Start_Twice_Fails(t *testing.T) {
	runner := &mockRunner{}
	w := newStartedWorker(t, runner)

	err := w.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	start, _, _ := runner.counts()
	assert.Equal(t, 1, start, "두 번째 Start 는 컨테이너를 추가 기동하지 않는다")
}

// TestWorker_Stop_BeforeStart_Noop 은 Start 전 Stop 이 에러 없이 noop 인지 검증합니다.
func TestWorker_Stop_BeforeStart_Noop(t *testing.T) {
	runner := &mockRunner{}
	w := newWorker(t, makeAuthDir(t), runner)

	require.NoError(t, w.Stop(t.Context()))

	_, _, stop := runner.counts()
	assert.Zero(t, stop, "기동된 컨테이너가 없으면 StopContainer 도 호출하지 않는다")
}

// TestWorker_Start_DockerFailure_Propagates 는 docker 기동 실패가 그대로 전파되는지 검증합니다.
func TestWorker_Start_DockerFailure_Propagates(t *testing.T) {
	runner := &mockRunner{startErr: errors.New("docker daemon unreachable")}
	w := newWorker(t, makeAuthDir(t), runner)

	err := w.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker daemon unreachable")
}

// TestWorker_Stop_Failure_KeepsWorkerUsable 는 StopContainer 실패 시 worker 상태가
// 복원되어 재시도 / 계속 사용이 가능한지 검증합니다.
//
// 이 복원이 없으면 stopping 플래그가 선 채로 남아, 컨테이너가 살아 있는데도 admit() 이
// 영구 거부하는 좀비 worker 가 됩니다.
func TestWorker_Stop_Failure_KeepsWorkerUsable(t *testing.T) {
	runner := &mockRunner{stopErr: errors.New("docker rm failed"), execStdout: okOutput("h1.title")}
	w := newWorker(t, makeAuthDir(t), runner)
	require.NoError(t, w.Start(t.Context()))

	err := w.Stop(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker rm failed")

	// 상태가 복원됐다면 세션 호출이 여전히 받아들여진다.
	_, err = w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.NoError(t, err, "Stop 실패 후에도 worker 는 사용 가능해야 한다")

	// 두 번째 Stop 은 재시도 — 이번엔 성공시킨다.
	runner.stopErr = nil
	require.NoError(t, w.Stop(context.Background()))
}

// TestWorker_ExtractEnriched_BeforeStart_Fails 는 기동 전 세션 호출이 거부되는지 검증합니다.
func TestWorker_ExtractEnriched_BeforeStart_Fails(t *testing.T) {
	runner := &mockRunner{}
	w := newWorker(t, makeAuthDir(t), runner)

	_, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not started")

	_, exec, _ := runner.counts()
	assert.Zero(t, exec)
}

// TestWorker_ExtractEnriched_ExecFailure_IncludesStderr 는 exec 실패 시 stderr 미리보기가
// 에러에 포함되는지 검증합니다 — 운영자가 컨테이너 로그를 따로 뒤지지 않아도 되게 하는 장치.
func TestWorker_ExtractEnriched_ExecFailure_IncludesStderr(t *testing.T) {
	runner := &mockRunner{
		execErr:    errors.New("exit status 1"),
		execStderr: "codex: authentication required",
	}
	w := newStartedWorker(t, runner)

	_, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
	assert.Contains(t, err.Error(), "authentication required")
}

// TestWorker_ExtractEnriched_ExecArgs 는 codex CLI 비대화 모드 호출 형태를 고정합니다.
//
// claude backend 와 달리 `codex exec` 하위명령을 쓰며, 프롬프트는 플래그가 아니라
// **마지막 위치 인자** 로 전달됩니다. 이 형태가 깨지면 CLI 가 인자 파싱 단계에서 실패합니다.
func TestWorker_ExtractEnriched_ExecArgs(t *testing.T) {
	runner := &mockRunner{execStdout: okOutput("h1.headline")}
	w := newStartedWorker(t, runner)

	_, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.NoError(t, err)

	args := runner.execArgs()
	require.GreaterOrEqual(t, len(args), 5)
	assert.Equal(t, []string{"codex", "exec", "--model", "gpt-5-codex"}, args[:4])
	assert.Contains(t, args[len(args)-1], "example.com", "프롬프트가 마지막 위치 인자")
	assert.Contains(t, args[len(args)-1], "/workspace/", "세션 경로가 프롬프트에 주입된다")
}

// TestWorker_Extract_Blacklist_ReturnsError 는 blacklist 판정이 Extract (thin wrapper)
// 에서 에러로 표면화되는지 검증합니다 — 셀렉터가 없는 상태로 성공 반환되면 안 됩니다.
func TestWorker_Extract_Blacklist_ReturnsError(t *testing.T) {
	runner := &mockRunner{
		execStdout: `{"validity":"blacklist","blacklist_reason":"index page","blacklist_mode":"extract_links_only"}`,
	}
	w := newStartedWorker(t, runner)

	_, err := w.Extract(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index page")
}

// TestWorker_ExtractEnriched_Timeout 은 세션 타임아웃이 exec 를 끊는지 검증합니다.
func TestWorker_ExtractEnriched_Timeout(t *testing.T) {
	runner := &mockRunner{execBlocks: true}
	// 기본 세션 타임아웃(10s)을 기다리지 않도록 짧은 상한의 worker 를 쓴다.
	w := shortTimeoutWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	start := time.Now()
	_, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second, "세션 타임아웃이 exec 를 끊어야 한다")
}

// ── 출력 파싱 ────────────────────────────────────────────────────────────────

// TestWorker_ExtractEnriched_ParsesOkOutput 은 validity=ok 응답이 ExtractResult 로
// 정확히 매핑되는지 검증합니다.
//
// 에러 부재만 보면 selectors 가 통째로 비어도 통과하므로 값까지 단언합니다.
func TestWorker_ExtractEnriched_ParsesOkOutput(t *testing.T) {
	runner := &mockRunner{execStdout: "chatter before\n" + okOutput("h1.headline") + "\ntrailing"}
	w := newStartedWorker(t, runner)

	res, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.NoError(t, err)
	require.Nil(t, res.Blacklist)
	require.NotNil(t, res.Selectors.Title)
	assert.Equal(t, "h1.headline", res.Selectors.Title.CSS)
	require.NotNil(t, res.Selectors.MainContent)
	assert.Equal(t, "div.body", res.Selectors.MainContent.CSS)
	assert.InDelta(t, 0.9, res.PageTypeConfidence, 0.0001)
	assert.True(t, res.Article)
}

// TestWorker_ExtractEnriched_BlacklistModeNormalized 는 LLM 응답의 case 변종이
// 정규화되는지 검증합니다 — 대문자 "DROP" 이 unknown 으로 떨어지면 link harvest
// fallback 이 의도치 않게 막힙니다.
func TestWorker_ExtractEnriched_BlacklistModeNormalized(t *testing.T) {
	runner := &mockRunner{
		execStdout: `{"validity":"blacklist","blacklist_reason":"login wall","blacklist_mode":"EXTRACT_LINKS_ONLY"}`,
	}
	w := newStartedWorker(t, runner)

	res, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.NoError(t, err)
	require.NotNil(t, res.Blacklist)
	assert.Equal(t, "login wall", res.Blacklist.Reason)
	assert.Equal(t, model.BlacklistModeExtractLinksOnly, res.Blacklist.Mode)
}

// TestWorker_ExtractEnriched_BlacklistWithoutReason 은 reason 누락 시 자리표시자가
// 채워지는지 검증합니다 — 빈 reason 이 DB 로 흘러가면 운영자가 원인을 알 수 없습니다.
func TestWorker_ExtractEnriched_BlacklistWithoutReason(t *testing.T) {
	runner := &mockRunner{execStdout: `{"validity":"blacklist","blacklist_reason":"  "}`}
	w := newStartedWorker(t, runner)

	res, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.NoError(t, err)
	require.NotNil(t, res.Blacklist)
	assert.NotEmpty(t, res.Blacklist.Reason)
}

// TestWorker_ExtractEnriched_NoJSON_Fails 는 JSON 블록이 없는 출력이 에러가 되는지 검증합니다.
func TestWorker_ExtractEnriched_NoJSON_Fails(t *testing.T) {
	runner := &mockRunner{execStdout: "I could not analyze this page."}
	w := newStartedWorker(t, runner)

	_, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse codex output")
}

// TestWorker_ExtractEnriched_UnknownValidity_Fails 는 알 수 없는 validity 값이
// 조용히 성공으로 처리되지 않는지 검증합니다.
func TestWorker_ExtractEnriched_UnknownValidity_Fails(t *testing.T) {
	runner := &mockRunner{execStdout: `{"validity":"maybe"}`}
	w := newStartedWorker(t, runner)

	_, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validity")
}

// TestWorker_ExtractEnriched_ListTargetUsesListPrompt 는 target type 에 따라 다른
// prompt asset 이 선택되는지 검증합니다.
func TestWorker_ExtractEnriched_ListTargetUsesListPrompt(t *testing.T) {
	runner := &mockRunner{execStdout: okOutput("h1.a")}
	w := newStartedWorker(t, runner)

	_, err := w.ExtractEnriched(t.Context(), "example.com", model.TargetTypeList, "<html></html>")
	require.NoError(t, err)

	args := runner.execArgs()
	assert.Contains(t, args[len(args)-1], "list JSON", "list target 은 list prompt 를 쓴다")
}
