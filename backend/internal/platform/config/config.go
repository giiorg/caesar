// Package config loads caesar's single strict JSON config file. Unknown keys
// are rejected (default-deny). Environment overrides exist for bootstrap
// secrets only: DATABASE_URL and CAESAR_MASTER_KEY.
// This is the only package allowed to touch the process environment
// (enforced by the envaccess analyzer).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

type Config struct {
	Server       Server       `json:"server"`
	Database     Database     `json:"database"`
	Log          Log          `json:"log"`
	Auth         Auth         `json:"auth"`
	Security     Security     `json:"security"`
	Orchestrator Orchestrator `json:"orchestrator"`
	Runner       Runner       `json:"runner"`
}

type Server struct {
	Addr            string   `json:"addr"`
	ShutdownTimeout Duration `json:"shutdown_timeout"`
	AppOrigin       string   `json:"app_origin"`
}

type Database struct {
	URL string `json:"url"`
}

type Auth struct {
	SessionIdleTTL     Duration `json:"session_idle_ttl"`
	SessionAbsoluteTTL Duration `json:"session_absolute_ttl"`
}

type Log struct {
	Level string `json:"level"`
}

// Security carries the credential master keyset (spec §8 bootstrap
// secret): "id:base64key[,id:base64key...]", first entry active. The
// CAESAR_MASTER_KEY env var overrides; serve and admin rekey require it,
// migrate and bootstrap do not.
type Security struct {
	MasterKey string `json:"master_key"`
}

// Orchestrator tunes the execution spine's queue loop: worker
// parallelism, poll cadence, lease lifetimes, and outbox retention.
type Orchestrator struct {
	Workers           int      `json:"workers"`
	PollInterval      Duration `json:"poll_interval"`
	LeaseTTL          Duration `json:"lease_ttl"`
	HeartbeatInterval Duration `json:"heartbeat_interval"`
	OutboxRetention   Duration `json:"outbox_retention"`
}

// Runner configures the Docker execution runner (Plan 06). It ships
// disabled: production intake is refused until Plan 08's forge adapters, so
// enabling it is an explicit operator act. Every byte/cpu/pids knob has a
// pinned default (plan Global Constraints); zero never means "unlimited".
type Runner struct {
	Enabled             bool     `json:"enabled"`
	DataRoot            string   `json:"data_root"`
	DataHostRoot        string   `json:"data_host_root"`
	CaesarImage         string   `json:"caesar_image"`
	GitImage            string   `json:"git_image"`
	EgressNetwork       string   `json:"egress_network"`
	Cpus                float64  `json:"cpus"`
	MemoryBytes         Bytes    `json:"memory_bytes"`
	WorkspaceQuotaBytes Bytes    `json:"workspace_quota_bytes"`
	WorkspaceQuotaFiles int64    `json:"workspace_quota_files"`
	LogCapBytes         Bytes    `json:"log_cap_bytes"`
	LogFloodBytes       Bytes    `json:"log_flood_bytes"`
	FreeBytesWatermark  Bytes    `json:"free_bytes_watermark"`
	PidsLimit           int64    `json:"pids_limit"`
	QuotaPollInterval   Duration `json:"quota_poll_interval"`
	ConnectAllow        []string `json:"connect_allow"`
}

// minRunnerPids floors pids_limit well above a shell + agent's needs so an
// operator can't accidentally starve the container's process table.
// maxRunnerPids caps it (plan Global Constraints) so the process table can't
// be sized into a local fork-bomb amplifier.
const (
	minRunnerPids = 64
	maxRunnerPids = 4096
)

// Duration is a time.Duration that unmarshals from JSON strings like "15s".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf(`duration must be a string like "15s": %w`, err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

func defaults() Config {
	return Config{
		Server: Server{Addr: ":8080", ShutdownTimeout: Duration(15 * time.Second), AppOrigin: "http://localhost:8080"},
		Log:    Log{Level: "info"},
		Auth:   Auth{SessionIdleTTL: Duration(14 * 24 * time.Hour), SessionAbsoluteTTL: Duration(90 * 24 * time.Hour)},
		Orchestrator: Orchestrator{
			Workers:           4,
			PollInterval:      Duration(500 * time.Millisecond),
			LeaseTTL:          Duration(30 * time.Second),
			HeartbeatInterval: Duration(10 * time.Second),
			OutboxRetention:   Duration(168 * time.Hour),
		},
		Runner: Runner{
			DataRoot:            "/var/lib/caesar",
			EgressNetwork:       "caesar-egress",
			Cpus:                2.0,
			MemoryBytes:         Bytes(4 << 30),
			WorkspaceQuotaBytes: Bytes(2 << 30),
			WorkspaceQuotaFiles: 200_000,
			LogCapBytes:         Bytes(16 << 20),
			LogFloodBytes:       Bytes(64 << 20),
			FreeBytesWatermark:  Bytes(10 << 30),
			PidsLimit:           512,
			QuotaPollInterval:   Duration(2 * time.Second),
		},
	}
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: open: %w", err)
	}
	defer f.Close()

	cfg := defaults()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if url := os.Getenv("DATABASE_URL"); url != "" {
		cfg.Database.URL = url
	}
	if key := os.Getenv("CAESAR_MASTER_KEY"); key != "" {
		cfg.Security.MasterKey = key
	}

	// data_host_root is the daemon-side spelling of data_root; when the
	// control plane and daemon share a filesystem the two coincide, so an
	// unset host root mirrors the local one rather than forcing operators to
	// repeat it.
	if cfg.Runner.DataHostRoot == "" {
		cfg.Runner.DataHostRoot = cfg.Runner.DataRoot
	}

	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// StageEnvFromProcess forwards the git auth env config into a constructed
// child env (envaccess law: this package alone reads the environment). The
// forge credential reaches the staging container this way and no other, so it
// never exists as a file anywhere in the attempt.
func StageEnvFromProcess() []string {
	var out []string
	for _, k := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// EgressConfigEnv carries the whole configuration of one attempt's egress
// gateway. The gateway runs as its own process in its own container with no
// config file and no database, so this variable is all it is given. It is
// exported because the runner constructs that container's environment and
// must name the same variable the gateway reads — one spelling, one place.
const EgressConfigEnv = "CAESAR_EGRESS_CONFIG"

// EgressProxyEnv returns that document verbatim. Decoding it belongs to the
// package that owns the gateway's vocabulary; reading the environment belongs
// here, and nowhere else (envaccess law).
func EgressProxyEnv() (string, error) {
	raw := os.Getenv(EgressConfigEnv)
	if raw == "" {
		return "", fmt.Errorf("config: %s is required: the egress gateway is configured entirely through it", EgressConfigEnv)
	}
	return raw, nil
}

func (c Config) validate() error {
	var problems []string
	if c.Server.Addr == "" {
		problems = append(problems, "server.addr must not be empty")
	}
	if c.Database.URL == "" {
		problems = append(problems, "database.url is required (file or DATABASE_URL)")
	}
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[c.Log.Level] {
		problems = append(problems, fmt.Sprintf("log.level %q is not one of debug|info|warn|error", c.Log.Level))
	}
	if c.Server.AppOrigin == "" {
		problems = append(problems, "server.app_origin must not be empty")
	}
	if c.Auth.SessionIdleTTL.Std() <= 0 {
		problems = append(problems, "auth.session_idle_ttl must be positive")
	}
	if c.Auth.SessionAbsoluteTTL.Std() < c.Auth.SessionIdleTTL.Std() {
		problems = append(problems, "auth.session_absolute_ttl must be >= auth.session_idle_ttl")
	}
	if c.Orchestrator.Workers < 1 || c.Orchestrator.Workers > 64 {
		problems = append(problems, "orchestrator.workers must be 1-64")
	}
	if c.Orchestrator.PollInterval.Std() <= 0 {
		problems = append(problems, "orchestrator.poll_interval must be positive")
	}
	if c.Orchestrator.LeaseTTL.Std() <= 0 {
		problems = append(problems, "orchestrator.lease_ttl must be positive")
	}
	if c.Orchestrator.HeartbeatInterval.Std() <= 0 {
		problems = append(problems, "orchestrator.heartbeat_interval must be positive")
	}
	if c.Orchestrator.OutboxRetention.Std() <= 0 {
		problems = append(problems, "orchestrator.outbox_retention must be positive")
	}
	if c.Orchestrator.HeartbeatInterval.Std() >= c.Orchestrator.LeaseTTL.Std() {
		problems = append(problems, "orchestrator.heartbeat_interval must be below orchestrator.lease_ttl")
	}
	if c.Security.MasterKey != "" {
		if _, err := secrets.ParseKeyset(c.Security.MasterKey); err != nil {
			problems = append(problems, fmt.Sprintf("security.master_key: %v", err))
		}
	}
	// Runner: caesar_image is only meaningful once enabled; the byte, cpu,
	// and pids invariants hold whether enabled or not (the shipped defaults
	// satisfy them), so a disabled block still fails closed on a bad knob.
	if c.Runner.Enabled && c.Runner.CaesarImage == "" {
		problems = append(problems, "runner.caesar_image is required when runner.enabled is true")
	}
	if c.Runner.Enabled && c.Runner.GitImage == "" {
		problems = append(problems, "runner.git_image is required when runner.enabled is true")
	}
	if c.Runner.LogFloodBytes.Int64() < c.Runner.LogCapBytes.Int64() {
		problems = append(problems, "runner.log_flood_bytes must be >= runner.log_cap_bytes")
	}
	if c.Runner.Cpus <= 0 || c.Runner.Cpus > 64 {
		problems = append(problems, "runner.cpus must be greater than 0 and at most 64")
	}
	if c.Runner.PidsLimit < minRunnerPids {
		problems = append(problems, fmt.Sprintf("runner.pids_limit must be at least %d", minRunnerPids))
	}
	if c.Runner.PidsLimit > maxRunnerPids {
		problems = append(problems, fmt.Sprintf("runner.pids_limit must be at most %d", maxRunnerPids))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
