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

func TestConfig_RunnerDefaultsAndValidation(t *testing.T) {
	t.Run("defaults when block absent and disabled", func(t *testing.T) {
		cfg, err := Load(writeFile(t, `{"database": {"url": "postgres://x"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Runner.Enabled {
			t.Error("runner must ship disabled")
		}
		if cfg.Runner.DataRoot != "/var/lib/caesar" {
			t.Errorf("default DataRoot = %q, want /var/lib/caesar", cfg.Runner.DataRoot)
		}
		if cfg.Runner.WorkspaceQuotaBytes.Int64() != 2<<30 {
			t.Errorf("default WorkspaceQuotaBytes = %d, want 2GiB", cfg.Runner.WorkspaceQuotaBytes.Int64())
		}
		if cfg.Runner.LogCapBytes.Int64() != 16<<20 {
			t.Errorf("default LogCapBytes = %d, want 16MiB", cfg.Runner.LogCapBytes.Int64())
		}
		if cfg.Runner.FreeBytesWatermark.Int64() != 10<<30 {
			t.Errorf("default FreeBytesWatermark = %d, want 10GiB", cfg.Runner.FreeBytesWatermark.Int64())
		}
		if cfg.Runner.Cpus != 2.0 {
			t.Errorf("default Cpus = %v, want 2.0", cfg.Runner.Cpus)
		}
		if cfg.Runner.PidsLimit != 512 {
			t.Errorf("default PidsLimit = %d, want 512", cfg.Runner.PidsLimit)
		}
		if cfg.Runner.EgressNetwork != "caesar-egress" {
			t.Errorf("default EgressNetwork = %q, want caesar-egress", cfg.Runner.EgressNetwork)
		}
	})

	t.Run("data_host_root empty defaults to data_root", func(t *testing.T) {
		cfg, err := Load(writeFile(t, `{"database": {"url": "postgres://x"}, "runner": {"data_root": "/data/caesar"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Runner.DataHostRoot != "/data/caesar" {
			t.Errorf("DataHostRoot = %q, want it to default to DataRoot /data/caesar", cfg.Runner.DataHostRoot)
		}
	})

	t.Run("data_host_root honoured when set", func(t *testing.T) {
		cfg, err := Load(writeFile(t, `{"database": {"url": "postgres://x"}, "runner": {"data_root": "/local", "data_host_root": "/host"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Runner.DataHostRoot != "/host" {
			t.Errorf("DataHostRoot = %q, want /host", cfg.Runner.DataHostRoot)
		}
	})

	t.Run("fails closed", func(t *testing.T) {
		tests := []struct{ name, block, wantErr string }{
			{"enabled without caesar_image", `{"enabled": true, "git_image": "caesar-git:dev"}`, "runner.caesar_image"},
			{"enabled without git_image", `{"enabled": true, "caesar_image": "caesar:dev"}`, "runner.git_image"},
			{"log_flood below log_cap", `{"log_cap_bytes": "16MiB", "log_flood_bytes": "8MiB"}`, "runner.log_flood_bytes"},
			{"cpus zero", `{"cpus": 0}`, "runner.cpus"},
			{"cpus over ceiling", `{"cpus": 65}`, "runner.cpus"},
			{"pids_limit below floor", `{"pids_limit": 8}`, "runner.pids_limit"},
			{"pids_limit above ceiling", `{"pids_limit": 8192}`, "runner.pids_limit"},
			{"unknown key inside runner", `{"wat": 1}`, "unknown field"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				body := `{"database": {"url": "postgres://x"}, "runner": ` + tt.block + `}`
				_, err := Load(writeFile(t, body))
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want mention of %q", err, tt.wantErr)
				}
			})
		}
	})

	t.Run("enabled with caesar_image loads", func(t *testing.T) {
		body := `{"database": {"url": "postgres://x"}, "runner": {"enabled": true, "caesar_image": "caesar:dev", "git_image": "caesar-git:dev"}}`
		cfg, err := Load(writeFile(t, body))
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Runner.Enabled || cfg.Runner.CaesarImage != "caesar:dev" {
			t.Fatalf("runner = %+v, want enabled with caesar:dev", cfg.Runner)
		}
	})
}

func TestConfig_ForgeDefaultsAndValidation(t *testing.T) {
	t.Run("defaults when block absent", func(t *testing.T) {
		cfg, err := Load(writeFile(t, `{"database": {"url": "postgres://x"}}`))
		if err != nil {
			t.Fatal(err)
		}
		// The API host and the clone host are DIFFERENT for GitHub and the same
		// for GitLab; getting that backwards is the silent self-hosted failure
		// in miniature, so both pairs are pinned.
		want := Forge{
			GitHubBaseURL: "https://api.github.com", GitLabBaseURL: "https://gitlab.com",
			GitHubCloneBaseURL: "https://github.com", GitLabCloneBaseURL: "https://gitlab.com",
			PublishWorkDir: "/var/lib/caesar/publish", RequestTimeout: Duration(30 * time.Second),
		}
		if diff := cmp.Diff(want, cfg.Forge); diff != "" {
			t.Errorf("forge defaults (-want +got):\n%s", diff)
		}
	})

	t.Run("a self-hosted base is honoured", func(t *testing.T) {
		body := `{"database": {"url": "postgres://x"}, "forge": {` +
			`"github_base_url": "https://ghe.example.com/api/v3", ` +
			`"github_clone_base_url": "https://ghe.example.com"}}`
		cfg, err := Load(writeFile(t, body))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Forge.GitHubBaseURL != "https://ghe.example.com/api/v3" ||
			cfg.Forge.GitHubCloneBaseURL != "https://ghe.example.com" {
			t.Fatalf("forge = %+v, want the configured GHE pair", cfg.Forge)
		}
	})

	t.Run("fails closed", func(t *testing.T) {
		// Every one of these is silent without the check: a bad base URL is
		// `retryable` to the forge client, so it becomes a transient
		// forge_unavailable retried on the schedule until the run's wall
		// deadline, with the cause buried in a run event.
		tests := []struct{ name, block, wantErr string }{
			{"no scheme", `{"github_base_url": "api.github.com"}`, "github_base_url"},
			{"typo'd scheme", `{"github_base_url": "htps://api.github.com"}`, "github_base_url"},
			{"no host", `{"gitlab_base_url": "https:///api"}`, "gitlab_base_url"},
			{"empty base", `{"github_clone_base_url": ""}`, "github_clone_base_url"},
			{"userinfo in a base", `{"gitlab_clone_base_url": "https://tok@gitlab.com"}`, "userinfo"},
			{"relative work dir", `{"publish_work_dir": "publish"}`, "publish_work_dir"},
			{"empty work dir", `{"publish_work_dir": ""}`, "publish_work_dir"},
			{"zero timeout", `{"request_timeout": "0s"}`, "request_timeout"},
			{"unknown key inside forge", `{"wat": 1}`, "unknown field"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				body := `{"database": {"url": "postgres://x"}, "forge": ` + tt.block + `}`
				_, err := Load(writeFile(t, body))
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want mention of %q", err, tt.wantErr)
				}
			})
		}
	})

	t.Run("http is allowed for a lab", func(t *testing.T) {
		body := `{"database": {"url": "postgres://x"}, "forge": {"gitlab_base_url": "http://gitlab.lab"}}`
		if _, err := Load(writeFile(t, body)); err != nil {
			t.Fatalf("err = %v; an air-gapped forge over http is a real deployment", err)
		}
	})
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
