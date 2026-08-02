// Package config loads caesar's single strict JSON config file. Unknown keys
// are rejected (default-deny). Environment overrides exist for bootstrap
// secrets only: DATABASE_URL here, CAESAR_MASTER_KEY when credentials land.
// This is the only package allowed to touch the process environment
// (enforced by the envaccess analyzer).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

type Config struct {
	Server   Server   `json:"server"`
	Database Database `json:"database"`
	Log      Log      `json:"log"`
}

type Server struct {
	Addr            string   `json:"addr"`
	ShutdownTimeout Duration `json:"shutdown_timeout"`
}

type Database struct {
	URL string `json:"url"`
}

type Log struct {
	Level string `json:"level"`
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
		Server: Server{Addr: ":8080", ShutdownTimeout: Duration(15 * time.Second)},
		Log:    Log{Level: "info"},
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

	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	var problems []string
	if c.Server.Addr == "" {
		problems = append(problems, "server.addr must not be empty")
	}
	if c.Database.URL == "" {
		problems = append(problems, "database.url is required (file or DATABASE_URL)")
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.Log.Level)); err != nil {
		problems = append(problems, fmt.Sprintf("log.level %q is not one of debug|info|warn|error", c.Log.Level))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
