package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "caesar.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const valid = `{
  "server": {"addr": ":9090", "shutdown_timeout": "5s"},
  "database": {"url": "postgres://u:p@localhost:5432/db"},
  "log": {"level": "debug"}
}`

func TestLoad_ParsesAllFields(t *testing.T) {
	cfg, err := Load(writeFile(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":9090" {
		t.Errorf("Server.Addr = %q, want %q", cfg.Server.Addr, ":9090")
	}
	if cfg.Server.ShutdownTimeout.Std() != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 5s", cfg.Server.ShutdownTimeout.Std())
	}
	if cfg.Database.URL != "postgres://u:p@localhost:5432/db" {
		t.Errorf("Database.URL = %q", cfg.Database.URL)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
	}
}

func TestLoad_RejectsUnknownKeys(t *testing.T) {
	tests := []struct{ name, body string }{
		{"top level", `{"database": {"url": "postgres://x"}, "wat": 1}`},
		{"nested", `{"database": {"url": "postgres://x", "wat": 1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("err = %v, want unknown-field rejection", err)
			}
		})
	}
}

func TestLoad_AppliesDefaultsForOmittedFields(t *testing.T) {
	cfg, err := Load(writeFile(t, `{"database": {"url": "postgres://x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("default Addr = %q, want :8080", cfg.Server.Addr)
	}
	if cfg.Server.ShutdownTimeout.Std() != 15*time.Second {
		t.Errorf("default ShutdownTimeout = %v, want 15s", cfg.Server.ShutdownTimeout.Std())
	}
	if cfg.Log.Level != "info" {
		t.Errorf("default Log.Level = %q, want info", cfg.Log.Level)
	}
	if cfg.Server.AppOrigin != "http://localhost:8080" {
		t.Errorf("default AppOrigin = %q, want http://localhost:8080", cfg.Server.AppOrigin)
	}
	if cfg.Auth.SessionIdleTTL.Std() != 14*24*time.Hour {
		t.Errorf("default SessionIdleTTL = %v, want 336h", cfg.Auth.SessionIdleTTL.Std())
	}
	if cfg.Auth.SessionAbsoluteTTL.Std() != 90*24*time.Hour {
		t.Errorf("default SessionAbsoluteTTL = %v, want 2160h", cfg.Auth.SessionAbsoluteTTL.Std())
	}
}

func TestLoad_ParsesExplicitAuthTTLs(t *testing.T) {
	body := `{"database": {"url": "postgres://x"}, "auth": {"session_idle_ttl": "1h", "session_absolute_ttl": "2h"}}`
	cfg, err := Load(writeFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.SessionIdleTTL.Std() != time.Hour {
		t.Errorf("SessionIdleTTL = %v, want 1h", cfg.Auth.SessionIdleTTL.Std())
	}
	if cfg.Auth.SessionAbsoluteTTL.Std() != 2*time.Hour {
		t.Errorf("SessionAbsoluteTTL = %v, want 2h", cfg.Auth.SessionAbsoluteTTL.Std())
	}
}

func TestLoad_DatabaseURLEnvOverridesFile(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://env-wins")
	cfg, err := Load(writeFile(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != "postgres://env-wins" {
		t.Errorf("Database.URL = %q, want env override", cfg.Database.URL)
	}
}

func TestLoad_FailsClosed(t *testing.T) {
	tests := []struct{ name, body, wantErr string }{
		{"missing database url", `{"server": {"addr": ":1"}}`, "database.url"},
		{"empty addr", `{"server": {"addr": ""}, "database": {"url": "postgres://x"}}`, "server.addr"},
		{"bad log level", `{"database": {"url": "postgres://x"}, "log": {"level": "loud"}}`, "log.level"},
		{"log level with offset +", `{"database": {"url": "postgres://x"}, "log": {"level": "info+4"}}`, "log.level"},
		{"log level with offset -", `{"database": {"url": "postgres://x"}, "log": {"level": "warn-100"}}`, "log.level"},
		{"bad duration", `{"server": {"shutdown_timeout": "fast"}, "database": {"url": "postgres://x"}}`, "duration"},
		{"malformed json", `{`, "parse"},
		{"absolute ttl less than idle ttl", `{"database": {"url": "postgres://x"}, "auth": {"session_idle_ttl": "2h", "session_absolute_ttl": "1h"}}`, "auth.session_absolute_ttl"},
		{"zero idle ttl", `{"database": {"url": "postgres://x"}, "auth": {"session_idle_ttl": "0s", "session_absolute_ttl": "1h"}}`, "auth.session_idle_ttl"},
		{"negative idle ttl", `{"database": {"url": "postgres://x"}, "auth": {"session_idle_ttl": "-1h", "session_absolute_ttl": "1h"}}`, "auth.session_idle_ttl"},
		{"empty app origin", `{"database": {"url": "postgres://x"}, "server": {"app_origin": ""}}`, "server.app_origin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want mention of %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_MissingFileErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("want error for missing file")
	}
}
