package prompt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm/prompt"
)

func TestNewFileLoader_NonExistentDir_ReturnsError(t *testing.T) {
	_, err := prompt.NewFileLoader("/nonexistent/path/should/not/exist")
	require.Error(t, err)
}

func TestNewFileLoader_NotADirectory_ReturnsError(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(tmp, []byte("x"), 0o600))

	_, err := prompt.NewFileLoader(tmp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestNewFileLoader_EmptyDir_ReturnsError(t *testing.T) {
	_, err := prompt.NewFileLoader("")
	require.Error(t, err)
}

func TestFileLoader_Load_Success(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "llmgen")
	require.NoError(t, os.MkdirAll(subdir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subdir, "system.txt"), []byte("hello world"), 0o600))

	l, err := prompt.NewFileLoader(dir)
	require.NoError(t, err)

	body, err := l.Load("llmgen/system")
	require.NoError(t, err)
	assert.Equal(t, "hello world", body)
}

func TestFileLoader_Load_FileMissing_ReturnsError(t *testing.T) {
	l, err := prompt.NewFileLoader(t.TempDir())
	require.NoError(t, err)

	_, err = l.Load("llmgen/missing")
	require.Error(t, err)
}

func TestFileLoader_Load_EmptyFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.txt"), []byte("   \n\t"), 0o600))

	l, err := prompt.NewFileLoader(dir)
	require.NoError(t, err)

	_, err = l.Load("empty")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestFileLoader_Load_EmptyName_ReturnsError(t *testing.T) {
	l, err := prompt.NewFileLoader(t.TempDir())
	require.NoError(t, err)

	_, err = l.Load("")
	require.Error(t, err)
}

func TestFileLoader_Load_CacheHit_NoSecondReadEvenAfterFileDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o600))

	l, err := prompt.NewFileLoader(dir)
	require.NoError(t, err)

	body1, err := l.Load("x")
	require.NoError(t, err)
	require.Equal(t, "first", body1)

	// 캐시 동작 검증 — 파일을 삭제해도 cache hit 으로 동일 결과 반환.
	require.NoError(t, os.Remove(path))

	body2, err := l.Load("x")
	require.NoError(t, err)
	assert.Equal(t, "first", body2, "cache 가 사용되어 disk read skip")
}

func TestMapLoader_Load_KnownKey(t *testing.T) {
	m := prompt.MapLoader{"a/b": "value"}
	body, err := m.Load("a/b")
	require.NoError(t, err)
	assert.Equal(t, "value", body)
}

func TestMapLoader_Load_UnknownKey_Errors(t *testing.T) {
	m := prompt.MapLoader{}
	_, err := m.Load("missing")
	require.Error(t, err)
}

func TestMapLoader_Load_EmptyValue_Errors(t *testing.T) {
	m := prompt.MapLoader{"empty": "  "}
	_, err := m.Load("empty")
	require.Error(t, err)
}

func TestRender_PlaceholderReplacement(t *testing.T) {
	out := prompt.Render(
		"Hello {{NAME}}, you have {{COUNT}} messages.",
		"{{NAME}}", "Alice",
		"{{COUNT}}", "3",
	)
	assert.Equal(t, "Hello Alice, you have 3 messages.", out)
}

func TestRender_NoReplacements_ReturnsTemplate(t *testing.T) {
	out := prompt.Render("unchanged template")
	assert.Equal(t, "unchanged template", out)
}

func TestRender_UnknownPlaceholder_LeftUntouched(t *testing.T) {
	out := prompt.Render("Hello {{NAME}} and {{UNKNOWN}}", "{{NAME}}", "World")
	assert.Equal(t, "Hello World and {{UNKNOWN}}", out)
}

func TestRender_OddLengthReplacements_Panics(t *testing.T) {
	assert.Panics(t, func() {
		prompt.Render("template", "{{KEY}}", "value", "{{ORPHAN}}")
	}, "Render 가 odd-length 인자에 명시적 panic")
}
