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
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
