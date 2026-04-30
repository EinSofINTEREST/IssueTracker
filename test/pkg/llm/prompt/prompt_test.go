package prompt_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/llm/prompt"
)

func TestLoad_TxtFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(prompt.EnvPromptsDir, dir)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi from txt"), 0o600))

	body, err := prompt.Load("hello")
	assert.NoError(t, err)
	assert.Equal(t, "hi from txt", body)
}

func TestLoad_MdFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(prompt.EnvPromptsDir, dir)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("# title"), 0o600))

	body, err := prompt.Load("hello")
	assert.NoError(t, err)
	assert.Equal(t, "# title", body)
}

func TestLoad_PrefersTxtOverMd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(prompt.EnvPromptsDir, dir)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("from txt"), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("from md"), 0o600))

	body, err := prompt.Load("hello")
	assert.NoError(t, err)
	assert.Equal(t, "from txt", body)
}

func TestLoad_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(prompt.EnvPromptsDir, dir)

	_, err := prompt.Load("missing")
	assert.ErrorIs(t, err, prompt.ErrNotFound)
}

func TestLoad_RejectsPathSeparator(t *testing.T) {
	_, err := prompt.Load("../etc/passwd")
	assert.Error(t, err)
	assert.False(t, errors.Is(err, prompt.ErrNotFound), "path traversal 은 ErrNotFound 가 아닌 별도 에러")
}

func TestLoad_EmptyName(t *testing.T) {
	_, err := prompt.Load("")
	assert.Error(t, err)
}
