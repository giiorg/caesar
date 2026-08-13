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
	"net/url"
	"os"
	"path/filepath"
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
	Forge        Forge        `json:"forge"`
	Realtime     Realtime     `json:"realtime"`
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
// disabled: it needs a Docker daemon, images and a writable data root on the
// host, so enabling it is an explicit operator act. Every byte/cpu/pids knob has a
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

// Forge is how caesar reaches its code hosts. Base URLs are configurable so
// self-hosted GitHub Enterprise and GitLab instances work without a code
// change; the defaults are the public endpoints.
//
// There are TWO endpoints per forge and they are genuinely different hosts,
// not one value used twice. GitHub's API lives at api.github.com while its
// repositories live at github.com; a GitHub Enterprise install typically
// serves both from one host under different paths. Configuring only the API
// base is the trap: every API call would reach the self-hosted instance while
// every clone and push still went to the public site, which reads as "the run
// can see the repo but publishes nothing" and is miserable to diagnose.
type Forge struct {
	GitHubBaseURL string `json:"github_base_url"`
	GitLabBaseURL string `json:"gitlab_base_url"`
	// The HTTPS roots a clone URL is built on — what `cloneURL` in
	// app/runnerports.go joins the repo slug onto, for both the staging clone
	// and the publication checkout.
	GitHubCloneBaseURL string `json:"github_clone_base_url"`
	GitLabCloneBaseURL string `json:"gitlab_clone_base_url"`
	// PublishWorkDir is where publication checkouts are materialized. Each
	// publication gets a fresh subdirectory that is removed when it ends,
	// so this only ever holds work in flight.
	PublishWorkDir string `json:"publish_work_dir"`
	// RequestTimeout bounds one forge API call. Unqualified: this struct
	// lives in package config, so config.Duration would not compile.
	RequestTimeout Duration `json:"request_timeout"`
}

// Realtime tunes the SSE streams (spec §4). Heartbeat honours the tunnel's
// idle timeout; SubscriberQueue bounds a slow client's cost — it is
// disconnected rather than allowed to backpressure the tailer.
//
// Every default here is repeated as a literal inside internal/realtime, so
// that a zero StreamerConfig/TailerConfig/HubConfig is usable rather than
// broken. That is two definitions of one number and it is deliberate; what
// keeps them equal is realtime's own defaults_test.go, which reads this
// block. Move a number here and that test says so.
type Realtime struct {
	// HeartbeatInterval is how often an idle stream is padded with an SSE
	// comment so the tunnel does not cull it.
	HeartbeatInterval Duration `json:"heartbeat_interval"`
	// PollFallback is how long the tailer waits for a LISTEN/NOTIFY wake-up
	// before reading the table anyway. It is the worst-case latency of a
	// notification that never arrived, and it is what carries correctness
	// while the listener's connection is down — never disable it.
	PollFallback Duration `json:"poll_fallback"`
	// GrantRecheck is how often an open stream re-resolves its principal's
	// scope, so it bounds how long a revoked grant keeps streaming.
	GrantRecheck Duration `json:"grant_recheck"`
	// SubscriberQueue is how many events one client may fall behind by
	// before it is disconnected and told to resume from Last-Event-ID.
	SubscriberQueue int `json:"subscriber_queue"`
	// ReplayBatch caps ONE catch-up page — an I/O chunk size, invisible to a
	// client except as the number of round trips its catch-up takes.
	ReplayBatch int `json:"replay_batch"`
	// SnapshotEvents is how much history a stream opens with: the most recent
	// N matching events, before any catch-up.
	//
	// It is NOT ReplayBatch and must not be folded into it even though both
	// default to 500. This one is what a client SEES on connect — a product
	// decision about how far back a freshly opened view goes — while
	// ReplayBatch is only how many rows one query fetches. Tying them would
	// mean an operator who halved a page size silently halved everyone's
	// backlog.
	SnapshotEvents int `json:"snapshot_events"`
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
		Forge: Forge{
			GitHubBaseURL:      "https://api.github.com",
			GitLabBaseURL:      "https://gitlab.com",
			GitHubCloneBaseURL: "https://github.com",
			GitLabCloneBaseURL: "https://gitlab.com",
			PublishWorkDir:     "/var/lib/caesar/publish",
			RequestTimeout:     Duration(30 * time.Second),
		},
		Realtime: Realtime{
			HeartbeatInterval: Duration(20 * time.Second),
			PollFallback:      Duration(2 * time.Second),
			GrantRecheck:      Duration(30 * time.Second),
			SubscriberQueue:   256,
			ReplayBatch:       500,
			SnapshotEvents:    500,
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

// forgeBaseProblems checks one forge base URL. `http` is allowed because a lab
// or an air-gapped GitLab is a real deployment; userinfo is refused outright
// because a credential in a base URL would ride into every request URL, and
// from there into logs, reflogs and process listings — the exact leak the
// token-as-header rule exists to prevent.
func forgeBaseProblems(field, raw string) []string {
	if raw == "" {
		return []string{field + " must not be empty"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return []string{fmt.Sprintf("%s %q is not a URL: %v", field, raw, err)}
	}
	var out []string
	if u.Scheme != "https" && u.Scheme != "http" {
		out = append(out, fmt.Sprintf("%s %q must use scheme https (or http for a lab)", field, raw))
	}
	if u.Host == "" {
		out = append(out, fmt.Sprintf("%s %q has no host", field, raw))
	}
	if u.User != nil {
		out = append(out, field+" must not carry userinfo: the forge token travels as a header, never in a URL")
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
	// Forge: fail closed at boot, because the alternative is silent and slow.
	// A base URL with a typo'd scheme is `retryable` to the forge client, so
	// it becomes a transient forge_unavailable that is RETRIED on the schedule
	// until the run's wall deadline, with the real cause buried in a run event.
	problems = append(problems,
		forgeBaseProblems("forge.github_base_url", c.Forge.GitHubBaseURL)...)
	problems = append(problems,
		forgeBaseProblems("forge.gitlab_base_url", c.Forge.GitLabBaseURL)...)
	problems = append(problems,
		forgeBaseProblems("forge.github_clone_base_url", c.Forge.GitHubCloneBaseURL)...)
	problems = append(problems,
		forgeBaseProblems("forge.gitlab_clone_base_url", c.Forge.GitLabCloneBaseURL)...)
	if !filepath.IsAbs(c.Forge.PublishWorkDir) {
		problems = append(problems, "forge.publish_work_dir must be a non-empty absolute path")
	}
	if c.Forge.RequestTimeout.Std() <= 0 {
		problems = append(problems, "forge.request_timeout must be positive")
	}
	// Realtime: each knob is a liveness property of a long-lived stream, and
	// the failure of a zero is silent rather than loud — a zero heartbeat is
	// a busy loop, a zero queue disconnects every subscriber on its first
	// event, a zero replay batch never finishes catching up, and a zero
	// snapshot opens every stream on an empty backlog, which reads to the
	// client as a run with no history.
	if c.Realtime.HeartbeatInterval.Std() <= 0 {
		problems = append(problems, "realtime.heartbeat_interval must be positive")
	}
	if c.Realtime.PollFallback.Std() <= 0 {
		problems = append(problems, "realtime.poll_fallback must be positive")
	}
	if c.Realtime.GrantRecheck.Std() <= 0 {
		problems = append(problems, "realtime.grant_recheck must be positive")
	}
	if c.Realtime.SubscriberQueue < 1 {
		problems = append(problems, "realtime.subscriber_queue must be positive")
	}
	if c.Realtime.ReplayBatch < 1 {
		problems = append(problems, "realtime.replay_batch must be positive")
	}
	if c.Realtime.SnapshotEvents < 1 {
		problems = append(problems, "realtime.snapshot_events must be positive")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
