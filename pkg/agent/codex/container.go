package codex

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// execContainerRunner 는 실제 docker CLI 를 사용하는 ContainerRunner 구현입니다.
type execContainerRunner struct{}

// StartContainer 는 workspace + auth 상태를 마운트한 장기 실행 컨테이너를 기동합니다.
//
// 마운트 정책 (claude 와 차이: sibling JSON 파일 마운트 없음 — codex 는 ~/.codex 단일 디렉토리만 사용):
//   - workDir → /workspace (read-write): 세션별 페이지 + 출력 임시 저장
//   - authDir → containerAuthPath (read-write): 호스트의 ~/.codex 디렉토리.
//     Codex CLI 가 세션 history / 일시 상태를 본 디렉토리에 기록하므로 :ro 마운트 불가.
//
// 컨테이너는 `tail -f /dev/null` 로 대기 — docker exec 세션이 올 때까지 유지.
func (r *execContainerRunner) StartContainer(ctx context.Context, image, workDir, authDir, containerAuthPath string) (string, error) {
	cleanAuthDir := filepath.Clean(authDir)
	cleanContainerAuthPath := filepath.Clean(containerAuthPath)

	args := []string{
		"run", "-d", "--rm",
		"-v", cleanAuthDir + ":" + cleanContainerAuthPath,
		"-v", workDir + ":/workspace",
		image,
		"tail", "-f", "/dev/null",
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// stderr 는 truncateStderrLen (512) 사용 — stdout (256) 과 분리 (gemini #3321915455).
		return "", fmt.Errorf("docker run: %w (stderr: %s)", err, truncate(stderr.String(), truncateStderrLen))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ExecSession 은 실행 중인 컨테이너에서 명령을 실행합니다.
// 인증은 컨테이너에 마운트된 auth_token 디렉토리로 처리됨 — env 전달 불필요.
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
