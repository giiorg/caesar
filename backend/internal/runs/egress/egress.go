// Package egress is the attempt's only route off its network: a broker that
// signs and forwards the provider calls the agent is allowed to make, plus a
// CONNECT proxy for allowlisted hosts. The agent container has no route of
// its own — everything it reaches, it reaches through a gateway configured
// here, default-deny in both halves.
package egress

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Op is one permitted provider operation. Anything the allowlist does not
// name is denied — the gateway is default-deny, so this table is the whole
// surface an agent can reach through its credential.
type Op struct {
	Method     string `json:"method"`
	PathPrefix string `json:"path_prefix"`
}

// Provider is the credential the gateway holds on the agent's behalf and the
// operations it will spend it on. The agent never sees Credential: it
// presents its own attempt token and the gateway swaps one for the other.
type Provider struct {
	Endpoint         string `json:"endpoint"` // "" = CONNECT-only mode (validation gateways)
	Credential       string `json:"credential"`
	CredentialHeader string `json:"credential_header"` // "x-api-key" | "authorization"
	CredentialPrefix string `json:"credential_prefix"` // "" | "Bearer "
	Ops              []Op   `json:"ops"`
}

// Config is one gateway's whole world. It arrives as a single JSON document
// in the gateway container's environment, so nothing about an attempt's
// egress posture is ambient or editable from inside the sandbox.
type Config struct {
	Attempt      string   `json:"attempt"`
	ListenAddr   string   `json:"listen_addr"` // default ":8080"
	TokenSHA256  string   `json:"token_sha256"`
	Provider     Provider `json:"provider"`
	ConnectAllow []string `json:"connect_allow"`
	MeterSeconds int      `json:"meter_seconds"` // default 30
}

const (
	defaultListenAddr   = ":8080"
	defaultMeterSeconds = 30
	tokenDigestLen      = 64 // sha256, hex
)

// LoadConfig decodes the gateway's configuration document strictly: an
// unknown key is a misconfigured security boundary, not a forward-compatible
// extension, so it fails the process rather than opening a gateway whose
// posture nobody can predict.
func LoadConfig(raw string) (Config, error) {
	cfg := Config{}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("egress: parse config: %w", err)
	}
	if dec.More() {
		return Config{}, errors.New("egress: parse config: trailing content after the config object")
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("egress: config: %w", err)
	}
	return cfg, nil
}

// applyDefaults fills the values a caller may leave zero and normalizes the
// token digest, so an uppercase digest cannot silently reject every token.
func (c *Config) applyDefaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = defaultListenAddr
	}
	if c.MeterSeconds == 0 {
		c.MeterSeconds = defaultMeterSeconds
	}
	c.TokenSHA256 = strings.ToLower(strings.TrimSpace(c.TokenSHA256))
	c.Provider.Endpoint = strings.TrimRight(c.Provider.Endpoint, "/")
}

// validate reports every problem at once; its messages quote field names and
// never values, so a bad config cannot echo the credential into a log.
func (c Config) validate() error {
	var problems []string
	if c.Attempt == "" {
		problems = append(problems, "attempt is required")
	}
	if c.ListenAddr == "" {
		problems = append(problems, "listen_addr must not be empty")
	}
	if c.MeterSeconds <= 0 {
		problems = append(problems, "meter_seconds must be positive")
	}
	if c.TokenSHA256 != "" {
		if _, err := hex.DecodeString(c.TokenSHA256); err != nil || len(c.TokenSHA256) != tokenDigestLen {
			problems = append(problems, "token_sha256 must be empty or 64 hex characters")
		}
	}
	if c.Provider.Endpoint != "" {
		u, err := url.Parse(c.Provider.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			problems = append(problems, "provider.endpoint must be an http(s) URL")
		}
		if c.Provider.CredentialHeader == "" {
			problems = append(problems, "provider.credential_header is required when provider.endpoint is set")
		}
	}
	for i, op := range c.Provider.Ops {
		// An op without a rooted prefix would open every path under that
		// method, which is the opposite of an allowlist.
		if op.Method == "" || !strings.HasPrefix(op.PathPrefix, "/") {
			problems = append(problems, fmt.Sprintf("provider.ops[%d] needs a method and a path_prefix starting with /", i))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
