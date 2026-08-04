package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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

func TestLoad_OrchestratorDefaultsWhenBlockAbsent(t *testing.T) {
	cfg, err := Load(writeFile(t, `{"database": {"url": "postgres://x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Orchestrator.Workers != 4 {
		t.Errorf("default Workers = %d, want 4", cfg.Orchestrator.Workers)
	}
	if cfg.Orchestrator.PollInterval.Std() != 500*time.Millisecond {
		t.Errorf("default PollInterval = %v, want 500ms", cfg.Orchestrator.PollInterval.Std())
	}
	if cfg.Orchestrator.LeaseTTL.Std() != 30*time.Second {
		t.Errorf("default LeaseTTL = %v, want 30s", cfg.Orchestrator.LeaseTTL.Std())
	}
	if cfg.Orchestrator.HeartbeatInterval.Std() != 10*time.Second {
		t.Errorf("default HeartbeatInterval = %v, want 10s", cfg.Orchestrator.HeartbeatInterval.Std())
	}
	if cfg.Orchestrator.OutboxRetention.Std() != 168*time.Hour {
		t.Errorf("default OutboxRetention = %v, want 168h", cfg.Orchestrator.OutboxRetention.Std())
	}
}

func TestLoad_OrchestratorFailsClosed(t *testing.T) {
	tests := []struct{ name, block, wantErr string }{
		{"zero workers", `{"workers": 0}`, "orchestrator.workers"},
		{"too many workers", `{"workers": 65}`, "orchestrator.workers"},
		{"heartbeat not below lease", `{"lease_ttl": "10s", "heartbeat_interval": "10s"}`, "orchestrator.heartbeat_interval"},
		{"zero poll interval", `{"poll_interval": "0s"}`, "orchestrator.poll_interval"},
		{"negative lease ttl", `{"lease_ttl": "-1s"}`, "orchestrator.lease_ttl"},
		{"zero outbox retention", `{"outbox_retention": "0h"}`, "orchestrator.outbox_retention"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"database": {"url": "postgres://x"}, "orchestrator": ` + tt.block + `}`
			_, err := Load(writeFile(t, body))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want mention of %q", err, tt.wantErr)
			}
		})
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

func TestLoad_MasterKeyFromFileAndEnvOverride(t *testing.T) {
	path := writeFile(t, `{
	  "database": {"url": "postgres://x"},
	  "security": {"master_key": "file:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.MasterKey != "file:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Fatalf("master key = %q", cfg.Security.MasterKey)
	}

	t.Setenv("CAESAR_MASTER_KEY", "env:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.MasterKey != "env:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Fatalf("env must override file, got %q", cfg.Security.MasterKey)
	}
}

func TestLoad_RejectsMalformedMasterKey(t *testing.T) {
	path := writeFile(t, `{
	  "database": {"url": "postgres://x"},
	  "security": {"master_key": "not-a-keyset"}
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("malformed keyset must fail validation")
	}
}

func TestLoad_MasterKeyIsOptionalAtLoadTime(t *testing.T) {
	// migrate/bootstrap run without it; serve enforces presence itself.
	path := writeFile(t, `{"database": {"url": "postgres://x"}}`)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestStageEnvFromProcess_ForwardsOnlyTheGitAuthKeysThatAreSet(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraheader")
	t.Setenv("GIT_CONFIG_VALUE_0", "")
	t.Setenv("DATABASE_URL", "postgres://must-not-be-forwarded")

	got := StageEnvFromProcess()
	want := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraheader"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("stage env mismatch (-want +got):\n%s", diff)
	}
}
