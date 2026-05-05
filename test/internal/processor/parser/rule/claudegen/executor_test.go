package claudegen_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/claudegen"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// mockRunner 는 docker 명령을 실행하지 않는 테스트용 구현입니다.
type mockRunner struct {
	stdout       string
	stderr       string
	err          error
	capturedArgs []string
}

func (m *mockRunner) Run(_ context.Context, args []string) (string, string, error) {
	m.capturedArgs = args
	return m.stdout, m.stderr, m.err
}

func newTestExecutor(runner *mockRunner) *claudegen.Executor {
	log := logger.New(logger.DefaultConfig())
	return claudegen.NewWithRunner(
		"ghcr.io/anthropics/claude-code:latest",
		"claude-sonnet-4-6",
		"test-api-key",
		10*time.Second,
		runner,
		log,
	)
}

func TestExtract_Success_ArticlePage(t *testing.T) {
	runner := &mockRunner{
		stdout: `Here are the selectors:
{"title":{"css":"h1.article-title"},"main_content":{"css":"div.article-body","multi":true},"published_at":{"css":"time","attribute":"datetime"}}`,
	}
	sm, err := newTestExecutor(runner).Extract(t.Context(), "news.naver.com", storage.TargetTypePage, "<html>...</html>")

	require.NoError(t, err)
	require.NotNil(t, sm.Title)
	assert.Equal(t, "h1.article-title", sm.Title.CSS)
	require.NotNil(t, sm.MainContent)
	assert.True(t, sm.MainContent.Multi)
	require.NotNil(t, sm.PublishedAt)
	assert.Equal(t, "datetime", sm.PublishedAt.Attribute)
}

func TestExtract_Success_ListPage(t *testing.T) {
	runner := &mockRunner{
		stdout: `{"item_container":{"css":"ul.news-list li"},"item_link":{"css":"a.news-link","attribute":"href"},"item_title":{"css":"span.title"}}`,
	}
	sm, err := newTestExecutor(runner).Extract(t.Context(), "news.daum.net", storage.TargetTypeList, "<html>...</html>")

	require.NoError(t, err)
	require.NotNil(t, sm.ItemContainer)
	assert.Equal(t, "ul.news-list li", sm.ItemContainer.CSS)
	require.NotNil(t, sm.ItemLink)
	assert.Equal(t, "href", sm.ItemLink.Attribute)
}

func TestExtract_DockerRunError(t *testing.T) {
	runner := &mockRunner{
		stderr: "docker: Cannot connect to the Docker daemon",
		err:    errors.New("exit status 1"),
	}
	_, err := newTestExecutor(runner).Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker run")
}

func TestExtract_NoJSONInOutput(t *testing.T) {
	runner := &mockRunner{
		stdout: "I could not find any relevant selectors in the provided HTML.",
	}
	_, err := newTestExecutor(runner).Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse claude output")
}

func TestExtract_DockerArgsContainAPIKey(t *testing.T) {
	runner := &mockRunner{stdout: `{"title":{"css":"h1"}}`}
	_, err := newTestExecutor(runner).Extract(t.Context(), "example.com", storage.TargetTypePage, "<html><h1>test</h1></html>")
	require.NoError(t, err)

	found := false
	for _, arg := range runner.capturedArgs {
		if arg == "ANTHROPIC_API_KEY=test-api-key" {
			found = true
			break
		}
	}
	assert.True(t, found, "ANTHROPIC_API_KEY must be passed to docker run args")
}

func TestExtract_ModelInArgs(t *testing.T) {
	runner := &mockRunner{stdout: `{"title":{"css":"h1"}}`}
	_, _ = newTestExecutor(runner).Extract(t.Context(), "example.com", storage.TargetTypePage, "<html></html>")

	modelFound := false
	for i, arg := range runner.capturedArgs {
		if arg == "--model" && i+1 < len(runner.capturedArgs) && runner.capturedArgs[i+1] == "claude-sonnet-4-6" {
			modelFound = true
			break
		}
	}
	assert.True(t, modelFound, "--model claude-sonnet-4-6 must be in docker args")
}

func TestNewFromEnv_MissingAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	log := logger.New(logger.DefaultConfig())
	_, err := claudegen.NewFromEnv(log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ANTHROPIC_API_KEY")
}
