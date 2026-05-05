package claudegen

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// execContainerRunner 는 실제 docker CLI 를 사용하는 ContainerRunner 구현입니다.
type execContainerRunner struct{}

// StartContainer 는 workspace 를 마운트한 장기 실행 컨테이너를 기동합니다.
// 컨테이너는 `tail -f /dev/null` 로 대기 — docker exec 세션이 올 때까지 유지.
// env 슬라이스의 값은 cmd.Env 로 주입 — args 에 포함하지 않아 ps 에 노출되지 않습니다.
func (r *execContainerRunner) StartContainer(ctx context.Context, image, workDir string, env []string) (string, error) {
	args := []string{
		"run", "-d", "--rm",
		"-e", "ANTHROPIC_API_KEY",
		"-v", workDir + ":/workspace",
		image,
		"tail", "-f", "/dev/null",
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker run: %w (stderr: %s)", err, truncate(stderr.String(), 256))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ExecSession 은 실행 중인 컨테이너에서 명령을 실행합니다.
// 컨테이너에 이미 ANTHROPIC_API_KEY 가 설정되어 있으므로 env 전달이 불필요합니다.
func (r *execContainerRunner) ExecSession(ctx context.Context, containerID string, args []string) (string, string, error) {
	fullArgs := append([]string{"exec", containerID}, args...)
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", fullArgs...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	return stdout.String(), stderr.String(), cmd.Run()
}

// StopContainer 는 컨테이너를 강제 종료하고 삭제합니다.
func (r *execContainerRunner) StopContainer(ctx context.Context, containerID string) error {
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", containerID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker rm -f: %w (output: %s)", err, truncate(string(out), 256))
	}
	return nil
}
