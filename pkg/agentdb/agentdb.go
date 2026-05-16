// Package agentdb 는 LLM agent (claude / codex 등) 가 read-only DB 에 직접 접근하기 위한
// 공통 추상을 제공합니다 (이슈 #472).
//
// 디자인 목표:
//   - DB credential 캡슐화 — agent backend (claude / codex) 가 변경되어도 동일 DSN 객체 재사용
//   - 전송 형식 (MCP / 직접 connect 등) 추상화 — 본 패키지가 변환을 담당, agent 는 결과만 받음
//   - 코드 외부에서는 항상 read-only 만 표현 — 잘못된 권한 주입 차단
//
// 본 패키지는 transport 무관한 데이터 모델 (DSN / MCPConfig) 만 정의. 실제 세션에 mount 하는
// 책임은 agent backend (예: pkg/agent/claude/enrich.go) 에 위임.
package agentdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// DSN 은 read-only Postgres 접속 정보를 캡슐화합니다.
//
// Password 는 caller (보통 config 로딩 시점) 에서 채워주며, 본 구조체 자체는 자격증명을
// 외부에 직접 노출하는 메소드를 제공하지 않습니다 — 모든 노출은 PostgresURI / MCPConfig
// 를 통해서만 (전달 형식 통제).
type DSN struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	SSLMode  string // "disable" / "require" / "verify-full" 등 (libpq 표기)
}

// Validate 는 필수 필드 누락을 확인합니다. config 로딩 직후 1회 호출 권장.
func (d DSN) Validate() error {
	if d.Host == "" {
		return errors.New("agentdb: dsn host empty")
	}
	if d.Port <= 0 || d.Port > 65535 {
		return fmt.Errorf("agentdb: dsn port out of range: %d", d.Port)
	}
	if d.Database == "" {
		return errors.New("agentdb: dsn database empty")
	}
	if d.User == "" {
		return errors.New("agentdb: dsn user empty")
	}
	// Password 는 dev 환경에서 비어있을 수 있으므로 강제하지 않음 (Postgres trust auth).
	return nil
}

// PostgresURI 는 libpq URI 표기로 직렬화합니다 — `postgresql://user:pass@host:port/db?sslmode=...`.
//
// password 등 자격증명을 그대로 포함하므로, 호출자는 본 결과가 어디에 기록되는지 (로그 / 파일 /
// network) 항상 인지해야 합니다.
func (d DSN) PostgresURI() string {
	userInfo := url.UserPassword(d.User, d.Password)
	u := url.URL{
		Scheme: "postgresql",
		User:   userInfo,
		Host:   fmt.Sprintf("%s:%d", d.Host, d.Port),
		Path:   "/" + d.Database,
	}
	if d.SSLMode != "" {
		q := u.Query()
		q.Set("sslmode", d.SSLMode)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// String 은 password 를 마스킹한 안전 표기를 반환합니다 — 로그/디버그 용.
func (d DSN) String() string {
	masked := d
	if masked.Password != "" {
		masked.Password = "***"
	}
	userInfo := masked.User
	if masked.Password != "" {
		userInfo = masked.User + ":" + masked.Password
	}
	return fmt.Sprintf("postgresql://%s@%s:%d/%s?sslmode=%s",
		userInfo, masked.Host, masked.Port, masked.Database, masked.SSLMode)
}

// DSNFromEnv 는 주어진 prefix 환경변수에서 read-only DSN 을 구성합니다.
//
// 환경변수 매핑 (prefix 예: "ENRICHER_DB_RO_"):
//   - <prefix>HOST      (default: localhost)
//   - <prefix>PORT      (default: 5432)
//   - <prefix>DATABASE  (필수)
//   - <prefix>USER      (필수)
//   - <prefix>PASSWORD  (선택 — Postgres trust auth 환경에서 빈 문자열 허용)
//   - <prefix>SSLMODE   (default: disable)
//
// 두 번째 반환값 ok 는 본 prefix 의 활성화 여부 — DATABASE / USER 둘 다 비어 있으면
// "미활성" 으로 보고 (false, nil) 반환. 부분 설정 (USER 만 / DATABASE 만) 은 misconfiguration
// 으로 간주하여 error 반환.
func DSNFromEnv(prefix string) (DSN, bool, error) {
	user := os.Getenv(prefix + "USER")
	db := os.Getenv(prefix + "DATABASE")
	if user == "" && db == "" {
		return DSN{}, false, nil
	}
	if user == "" || db == "" {
		return DSN{}, false, fmt.Errorf("agentdb: %sUSER and %sDATABASE must both be set", prefix, prefix)
	}

	host := os.Getenv(prefix + "HOST")
	if host == "" {
		host = "localhost"
	}
	port := 5432
	if raw := os.Getenv(prefix + "PORT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return DSN{}, false, fmt.Errorf("agentdb: parse %sPORT %q: %w", prefix, raw, err)
		}
		port = n
	}
	sslmode := os.Getenv(prefix + "SSLMODE")
	if sslmode == "" {
		sslmode = "disable"
	}

	d := DSN{
		Host:     host,
		Port:     port,
		Database: db,
		User:     user,
		Password: os.Getenv(prefix + "PASSWORD"),
		SSLMode:  sslmode,
	}
	if err := d.Validate(); err != nil {
		return DSN{}, false, err
	}
	return d, true, nil
}

// postgresMCPPackage 는 PostgresMCPConfig 가 npx 에 넘기는 패키지 spec — 버전 고정.
//
// 본 상수는 deployments/docker/claudegen/Dockerfile 의 MCP_POSTGRES_VERSION ARG 와 항상
// 같은 버전을 가리켜야 합니다 (이슈 #472 PR #473 coderabbit 지적). Dockerfile 이 사전 설치한
// 캐시를 npx 가 그대로 사용하도록 버전을 일치시켜 재현성 확보.
const postgresMCPPackage = "@modelcontextprotocol/server-postgres@0.6.2"

// MCPServerConfig 는 Model Context Protocol 의 server 항목 1개 구성입니다.
//
// claude code 의 `.mcp.json` schema 와 1:1 — claude 외 다른 MCP-호환 agent 도 동일 schema 를
// 사용하므로 transport 추상으로 채택 (이슈 #472).
type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPConfig 는 `.mcp.json` 파일에 직렬화될 최종 형태입니다.
type MCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// PostgresMCPConfig 는 read-only DSN 으로 `@modelcontextprotocol/server-postgres` 를 띄우는
// MCPConfig 를 생성합니다.
//
// serverName 은 agent 측에서 도구 이름 prefix 로 사용 (`mcp__<serverName>__query` 등).
// 권장: "issuetracker_ro" 처럼 의도가 명확한 이름.
//
// 본 함수는 DSN 을 검증한 뒤 URI 형태로 server-postgres 인자에 넘깁니다. server-postgres 가
// 자체적으로 read-only 모드를 강제하지 않으므로, role 자체의 SELECT-only 권한 (migration 031)
// 이 보안 layer 임에 주의.
func PostgresMCPConfig(serverName string, dsn DSN) (MCPConfig, error) {
	if strings.TrimSpace(serverName) == "" {
		return MCPConfig{}, errors.New("agentdb: serverName empty")
	}
	if err := dsn.Validate(); err != nil {
		return MCPConfig{}, err
	}
	return MCPConfig{
		MCPServers: map[string]MCPServerConfig{
			serverName: {
				Command: "npx",
				Args: []string{
					"-y",
					postgresMCPPackage,
					dsn.PostgresURI(),
				},
			},
		},
	}, nil
}

// Marshal 은 MCPConfig 를 `.mcp.json` 파일 내용으로 직렬화합니다.
//
// 결과 bytes 는 자격증명을 포함 — caller 가 출력 위치 (세션 디렉토리) 의 격리를 보장해야 함.
func (c MCPConfig) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("agentdb: marshal mcp config: %w", err)
	}
	return b, nil
}
