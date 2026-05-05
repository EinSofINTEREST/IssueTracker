package claudegen

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// execContainerRunner 는 실제 docker CLI 를 사용하는 ContainerRunner 구현입니다.
type execContainerRunner struct{}

// StartContainer 는 workspace + auth 상태를 마운트한 장기 실행 컨테이너를 기동합니다.
//
// 마운트 정책 (이슈 #266):
//   - workDir → /workspace (read-write): 세션별 페이지 + 출력 임시 저장
//   - authDir → containerAuthPath (read-write): 호스트의 .claude/ 디렉토리.
//     Claude CLI 가 세션 history / 일시 상태를 본 디렉토리에 기록하므로 :ro 마운트 불가.
//     운영자는 본 디렉토리를 통해 세션 기록이 누적될 수 있음을 인지해야 함.
//   - sibling .claude.json → containerAuthPath sibling (read-write, optional):
//     Claude 의 메인 설정 파일도 컨테이너가 갱신할 수 있어 read-write 로 마운트.
//     호스트에 파일이 존재할 때만 마운트 — 신규 사용자(claude CLI 미실행)는 .claude.json 부재 가능.
//
// 컨테이너는 `tail -f /dev/null` 로 대기 — docker exec 세션이 올 때까지 유지.
func (r *execContainerRunner) StartContainer(ctx context.Context, image, workDir, authDir, containerAuthPath string) (string, error) {
	// trailing slash 등 정규화 — sibling .claude.json 도출이 정확하도록 (PR #268 리뷰).
	cleanAuthDir := filepath.Clean(authDir)
	cleanContainerAuthPath := filepath.Clean(containerAuthPath)

	args := []string{
		"run", "-d", "--rm",
		"-v", cleanAuthDir + ":" + cleanContainerAuthPath,
	}

	// .claude.json 파일도 함께 마운트 (이슈 #266) — Claude CLI 가 main config 를 sibling 위치에서 찾음.
	hostJSON := filepath.Join(filepath.Dir(cleanAuthDir), ".claude.json")
	if _, err := os.Stat(hostJSON); err == nil {
		containerJSON := filepath.Join(filepath.Dir(cleanContainerAuthPath), ".claude.json")
		args = append(args, "-v", hostJSON+":"+containerJSON)
	} else if !os.IsNotExist(err) {
		// 권한 거부 등 다른 에러는 명시적으로 보고 — silent skip 회피.
		return "", fmt.Errorf("stat host claude config %q: %w", hostJSON, err)
	}

	args = append(args,
		"-v", workDir+":/workspace",
		image,
		"tail", "-f", "/dev/null",
	)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
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
