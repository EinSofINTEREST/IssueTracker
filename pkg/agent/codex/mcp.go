package codex

import (
	"fmt"
	"sort"
	"strings"

	agentdb "issuetracker/pkg/agent/dependency/db"
)

// mcpOverrideArgs 는 MCPConfig 를 codex CLI 의 `-c key=value` override 인자로 변환합니다
// (이슈 #585).
//
// # 왜 파일이 아니라 -c 인가
//
// codex 의 `exec` 하위명령에는 `--mcp-config` 같은 파일 주입 옵션이 **없다** (claude 의
// `.mcp.json` 방식과 다른 점). 설정을 넣는 경로는 두 가지뿐이다.
//
//  1. `codex mcp add` → `$CODEX_HOME/config.toml` 에 **영구 기록**
//  2. `-c mcp_servers.<name>.<field>=<TOML>` → **호출 단위, 비영구**
//
// 1번은 쓰면 안 된다. `$CODEX_HOME` 은 호스트의 `~/.codex` 가 RW 로 마운트된 곳이라,
// 컨테이너 안에서 등록하면 **호스트 설정 파일에 DSN 이 평문으로 영구 기록**된다. 세션이
// 끝나도 남고 다른 용도의 codex 사용에도 적용된다. 그래서 2번을 쓴다.
//
// # 노출 면
//
// `-c` 값은 argv 에 실린다 — 컨테이너 내부 `ps` 에서 보인다. claude 의 파일 mount 보다
// 노출 면이 넓다. 완화책으로 **DSN 류는 args 가 아니라 env 로 넘기는 것을 권장**한다
// (`agentdb.PostgresMCPConfig` 가 이미 그렇게 구성한다면 그대로 따라간다). 근본적으로는
// enricher_ro 가 SELECT-only role (migration 031) 이라는 점이 보안 layer 다.
//
// # 결정성
//
// map 순회 순서가 매번 달라지면 같은 설정이 다른 인자를 만들어 진단이 어려워지고 테스트도
// 불안정해진다. 서버 이름과 env 키를 정렬해 출력을 고정한다.
func mcpOverrideArgs(cfg *agentdb.MCPConfig) ([]string, error) {
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	var args []string
	for _, name := range names {
		if err := validateMCPServerName(name); err != nil {
			return nil, err
		}
		srv := cfg.MCPServers[name]
		if strings.TrimSpace(srv.Command) == "" {
			// command 가 없으면 codex 가 "invalid transport" 로 거부한다. 런타임까지
			// 미루지 않고 여기서 끊어 원인을 분명히 한다.
			return nil, fmt.Errorf("codex: mcp server %q has no command", name)
		}

		args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.command=%s", name, tomlString(srv.Command)))
		if len(srv.Args) > 0 {
			args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.args=%s", name, tomlStringArray(srv.Args)))
		}

		envKeys := make([]string, 0, len(srv.Env))
		for k := range srv.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		for _, k := range envKeys {
			if err := validateMCPEnvKey(name, k); err != nil {
				return nil, err
			}
			args = append(args, "-c",
				fmt.Sprintf("mcp_servers.%s.env.%s=%s", name, k, tomlString(srv.Env[k])))
		}
	}
	return args, nil
}

// validateMCPServerName 은 서버 이름이 TOML dotted-path 의 한 구간으로 안전한지 확인합니다.
//
// 점 / 공백 / 따옴표가 섞이면 key 경로가 의도치 않게 쪼개지거나 파싱이 깨진다. codex 는
// 그때 "invalid transport" 같은 간접적인 메시지를 내므로 원인 추적이 어렵다 — 여기서 막는다.
func validateMCPServerName(name string) error {
	if name == "" {
		return fmt.Errorf("codex: mcp server name must not be empty")
	}
	for _, r := range name {
		ok := r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("codex: mcp server name %q contains unsupported character %q "+
				"(allowed: letters, digits, '_', '-')", name, r)
		}
	}
	return nil
}

// validateMCPEnvKey 는 env 키가 dotted-path 구간으로 안전한지 확인합니다.
func validateMCPEnvKey(server, key string) error {
	if key == "" {
		return fmt.Errorf("codex: mcp server %q has an empty env key", server)
	}
	for _, r := range key {
		ok := r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("codex: mcp server %q env key %q contains unsupported character %q "+
				"(allowed: letters, digits, '_')", server, key, r)
		}
	}
	return nil
}

// tomlString 은 값을 TOML basic string 리터럴로 인용합니다.
//
// 인용을 빠뜨리면 codex 가 "TOML 파싱 실패 시 raw 문자열로 취급" 하는 경로로 떨어져,
// 배열이나 숫자가 **조용히 문자열로 격하**된다. 그 격하는 에러 없이 일어나므로 직접 인용한다.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlStringArray 는 문자열 slice 를 TOML 배열 리터럴로 직렬화합니다.
func tomlStringArray(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, it := range items {
		quoted = append(quoted, tomlString(it))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}
