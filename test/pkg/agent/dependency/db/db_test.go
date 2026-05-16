package db_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentdb "issuetracker/pkg/agent/dependency/db"
)

func TestDSN_PostgresURI_RoundTrip(t *testing.T) {
	d := agentdb.DSN{
		Host:     "db.example.com",
		Port:     5432,
		Database: "main",
		User:     "enricher_ro",
		Password: "p@ss/word",
		SSLMode:  "require",
	}
	uri := d.PostgresURI()

	// scheme + db + sslmode 검증.
	assert.True(t, strings.HasPrefix(uri, "postgresql://"))
	assert.Contains(t, uri, "@db.example.com:5432/main")
	assert.Contains(t, uri, "sslmode=require")
	// 특수문자 인코딩 — '/' 와 '@' 는 url.UserPassword 가 percent-encode.
	assert.Contains(t, uri, "p%40ss%2Fword")
}

func TestDSN_String_MasksPassword(t *testing.T) {
	d := agentdb.DSN{
		Host: "h", Port: 5432, Database: "m", User: "u", Password: "secret", SSLMode: "disable",
	}
	s := d.String()
	assert.NotContains(t, s, "secret")
	assert.Contains(t, s, "***")
}

func TestDSN_Validate(t *testing.T) {
	tests := []struct {
		name string
		in   agentdb.DSN
		want bool
	}{
		{"happy", agentdb.DSN{Host: "h", Port: 5432, Database: "d", User: "u"}, true},
		{"happy with sslmode", agentdb.DSN{Host: "h", Port: 5432, Database: "d", User: "u", SSLMode: "verify-full"}, true},
		{"missing host", agentdb.DSN{Port: 5432, Database: "d", User: "u"}, false},
		{"port zero", agentdb.DSN{Host: "h", Port: 0, Database: "d", User: "u"}, false},
		{"port too large", agentdb.DSN{Host: "h", Port: 70000, Database: "d", User: "u"}, false},
		{"missing db", agentdb.DSN{Host: "h", Port: 5432, User: "u"}, false},
		{"missing user", agentdb.DSN{Host: "h", Port: 5432, Database: "d"}, false},
		{"invalid sslmode", agentdb.DSN{Host: "h", Port: 5432, Database: "d", User: "u", SSLMode: "requre"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if tt.want {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestPostgresMCPConfig_StructureAndMarshal(t *testing.T) {
	d := agentdb.DSN{
		Host: "localhost", Port: 5432, Database: "main",
		User: "enricher_ro", Password: "pw", SSLMode: "disable",
	}
	cfg, err := agentdb.PostgresMCPConfig("issuetracker_ro", d)
	require.NoError(t, err)
	require.Contains(t, cfg.MCPServers, "issuetracker_ro")

	server := cfg.MCPServers["issuetracker_ro"]
	assert.Equal(t, "npx", server.Command)
	require.Len(t, server.Args, 3)
	assert.Equal(t, "-y", server.Args[0])
	// 패키지 spec 은 Dockerfile MCP_POSTGRES_VERSION 과 동일 버전 고정 — 변경 시 둘 다 업데이트.
	assert.True(t, strings.HasPrefix(server.Args[1], "@modelcontextprotocol/server-postgres@"),
		"package spec must be version-pinned (got %q)", server.Args[1])
	assert.Equal(t, d.PostgresURI(), server.Args[2])

	// .mcp.json 직렬화 — claude code 가 읽는 형태.
	b, err := cfg.Marshal()
	require.NoError(t, err)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &decoded))
	_, ok := decoded["mcpServers"]
	assert.True(t, ok, "marshaled config must contain mcpServers key")
}

func TestPostgresMCPConfig_RejectsEmptyServerName(t *testing.T) {
	_, err := agentdb.PostgresMCPConfig("", agentdb.DSN{
		Host: "h", Port: 5432, Database: "d", User: "u",
	})
	assert.Error(t, err)
}

func TestPostgresMCPConfig_RejectsInvalidDSN(t *testing.T) {
	_, err := agentdb.PostgresMCPConfig("name", agentdb.DSN{ /* empty */ })
	assert.Error(t, err)
}

func TestDSNFromEnv_HappyPath(t *testing.T) {
	t.Setenv("X_HOST", "db")
	t.Setenv("X_PORT", "5433")
	t.Setenv("X_DATABASE", "main")
	t.Setenv("X_USER", "u")
	t.Setenv("X_PASSWORD", "pw")
	t.Setenv("X_SSLMODE", "require")

	d, ok, err := agentdb.DSNFromEnv("X_")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "db", d.Host)
	assert.Equal(t, 5433, d.Port)
	assert.Equal(t, "main", d.Database)
	assert.Equal(t, "require", d.SSLMode)
}

func TestDSNFromEnv_Unset_ReturnsNotOK(t *testing.T) {
	d, ok, err := agentdb.DSNFromEnv("UNSET_PREFIX_")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, d.Host)
}

func TestDSNFromEnv_PartialConfig_IsError(t *testing.T) {
	t.Setenv("Y_USER", "u")
	// Y_DATABASE missing — 부분 설정은 misconfiguration.
	_, ok, err := agentdb.DSNFromEnv("Y_")
	assert.Error(t, err)
	assert.False(t, ok)
}

func TestDSNFromEnv_InvalidPort(t *testing.T) {
	t.Setenv("Z_USER", "u")
	t.Setenv("Z_DATABASE", "d")
	t.Setenv("Z_PORT", "not-a-number")
	_, ok, err := agentdb.DSNFromEnv("Z_")
	assert.Error(t, err)
	assert.False(t, ok)
}
