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

// StartContainer 는 workspace + auth 디렉토리를 마운트한 장기 실행 컨테이너를 기동합니다.
//
// 마운트 정책 (이슈 #266):
//   - workDir → /workspace (read-write): 세션별 페이지 + 출력 임시 저장
//   - authDir → containerAuthPath (read-only): 호스트의 Claude 구독 인증 토큰 — 컨테이너가 토큰 변조 못하도록 ro
//
// 컨테이너는 `tail -f /dev/null` 로 대기 — docker exec 세션이 올 때까지 유지.
// env 슬라이스의 값은 cmd.Env 로 주입 — args 에 포함하지 않아 ps 에 노출되지 않습니다.
func (r *execContainerRunner) StartContainer(ctx context.Context, image, workDir, authDir, containerAuthPath string, env []string) (string, error) {
	args := []string{
		"run", "-d", "--rm",
		"-v", authDir + ":" + containerAuthPath + ":ro",
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
		return "", fmt.Errorf("docker run: %w (stderr: %s)", err, truncate(stderr.String(), truncateStdoutLen))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ExecSession 은 실행 중인 컨테이너에서 명령을 실행합니다.
// 인증은 컨테이너에 마운트된 auth_token 디렉토리로 처리됨 (이슈 #266) — env 전달 불필요.
func (r *execContainerRunner) ExecSession(ctx context.Context, containerID string, args []string) (string, string, error) {
	fullArgs := append([]string{"exec", containerID}, args...)
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", fullArgs...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// cmd.Run() 을 먼저 실행 후 버퍼를 읽음 — return 문에서 평가 순서에 의존하지 않음.
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// StopContainer 는 컨테이너를 강제 종료하고 삭제합니다.
// 컨테이너가 --rm 플래그로 이미 자동 삭제된 경우 "no such container" 를 성공으로 처리합니다.
func (r *execContainerRunner) StopContainer(ctx context.Context, containerID string) error {
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", containerID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no such container") {
			return nil // 이미 제거됨 — 원하는 상태 달성
		}
		return fmt.Errorf("docker rm -f: %w (output: %s)", err, truncate(string(out), truncateStdoutLen))
	}
	return nil
}
