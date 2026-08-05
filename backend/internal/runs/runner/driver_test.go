package runner

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/egress"
)

// The pairing table is the whole point of the driver: a harness may only be
// handed a credential kind it can actually authenticate with, and how that
// credential reaches it — brokered at the gateway or materialized inside the
// container — follows from the pair, never from a default.
func TestDriver_CustodyDerivation(t *testing.T) {
	cases := []struct {
		harness, credKind string
		want              custody
		wantErr           bool
	}{
		{"claude-code", "subscription_token", custodyMaterialized, false},
		{"claude-code", "model_key", custodyBrokered, false},
		{"codex", "agent_auth_file", custodyMaterialized, false},
		{"codex", "model_key", custodyBrokered, false},
		{"claude-code", "agent_auth_file", "", true}, // wrong pairing, refused
		{"codex", "subscription_token", "", true},
		{"codex", "", "", true}, // an unbound attempt is not a brokered one
	}
	for _, c := range cases {
		t.Run(c.harness+"/"+c.credKind, func(t *testing.T) {
			d, err := driverFor(c.harness)
			if err != nil {
				t.Fatalf("driverFor(%s): %v", c.harness, err)
			}
			if d.kind != c.harness {
				t.Fatalf("driver kind = %q, want %q — a row must know which harness it is", d.kind, c.harness)
			}
			got, err := d.custodyOf(c.credKind)
			if (err != nil) != c.wantErr {
				t.Fatalf("custodyOf(%q) error = %v, wantErr %v", c.credKind, err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("custodyOf(%q) = %q, want %q", c.credKind, got, c.want)
			}
		})
	}
	if _, err := driverFor("gemini"); err == nil {
		t.Fatal("unknown harness kind must be refused, not defaulted")
	}
}

// Env delivery is per (harness, custody) and nothing else: the four cells are
// the whole contract the agent container sees.
func TestDriver_EnvComposition(t *testing.T) {
	cases := []struct {
		name    string
		harness string
		in      envInput
		want    []string
	}{
		{
			// The token is the credential; both kill-switches are what keep the
			// CLI inside the pinned allowlist (no telemetry, no self-update).
			name: "claude-code materialized",
			in:   envInput{custody: custodyMaterialized, secret: "sk-ant-oat01-x"},
			want: []string{
				"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-x",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
				"DISABLE_AUTOUPDATER=1",
			},
		},
		{
			// Brokered: the base URL points at the gateway and the api key is
			// the attempt token, so the provider credential never enters here.
			name: "claude-code brokered",
			in:   envInput{custody: custodyBrokered, brokerToken: "cbt1.abc"},
			want: []string{"ANTHROPIC_BASE_URL=http://egress:8080", "ANTHROPIC_API_KEY=cbt1.abc"},
		},
		{
			// Codex materialized carries no secret at all: the auth.json travels
			// via the mounted auth dir, and the env only redirects CODEX_HOME.
			name:    "codex materialized",
			harness: "codex",
			in:      envInput{custody: custodyMaterialized, secret: `{"tokens":{"refresh_token":"r"}}`},
			want:    []string{"CODEX_HOME=/caesar-auth"},
		},
		{
			name:    "codex brokered",
			harness: "codex",
			in:      envInput{custody: custodyBrokered, brokerToken: "cbt1.abc"},
			want:    []string{"OPENAI_BASE_URL=http://egress:8080/v1", "OPENAI_API_KEY=cbt1.abc"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind := c.harness
			if kind == "" {
				kind = "claude-code"
			}
			d, err := driverFor(kind)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, d.env(c.in)); diff != "" {
				t.Errorf("env mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// The CONNECT allowlist is the sole barrier for a materialized attempt, so it
// is pinned per harness and audited here: every host has a reason, and the
// telemetry endpoints both CLIs would otherwise reach are deliberately absent.
func TestDriver_ConnectAllowIsPinned(t *testing.T) {
	cases := []struct {
		kind     string
		want     []string
		excluded []string
	}{
		{
			kind:     "claude-code",
			want:     []string{"api.anthropic.com", "claude.ai", "platform.claude.com"},
			excluded: []string{"statsig.anthropic.com", "sentry.io", "api.anthropic.com:443"},
		},
		{
			kind:     "codex",
			want:     []string{"chatgpt.com", "auth.openai.com", "api.openai.com"},
			excluded: []string{"ab.chatgpt.com", "statsig.anthropic.com"},
		},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			d, err := driverFor(c.kind)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, d.connectAllow); diff != "" {
				t.Errorf("connect allowlist mismatch (-want +got):\n%s", diff)
			}
			for _, host := range c.excluded {
				if slices.Contains(d.connectAllow, host) {
					t.Errorf("%s must not be allowlisted", host)
				}
			}
		})
	}
}

// The gateway has exactly two shapes, and custody picks between them without
// a container in sight.
func TestGatewayConfig_Shapes(t *testing.T) {
	r := newSpecRunner(t, nil)
	r.opts.ConnectAllow = []string{"proxy.corp.internal"}

	t.Run("materialized runs open-CONNECT and holds nothing", func(t *testing.T) {
		d, err := driverFor("claude-code")
		if err != nil {
			t.Fatal(err)
		}
		spec := RunSpec{
			Ref:                testRef(),
			Custody:            custodyMaterialized,
			Harness:            HarnessSpec{Kind: "claude-code", ProviderEndpoint: "https://api.anthropic.com"},
			ProviderCredential: "sk-ant-oat01-x",
		}
		cfg := r.gatewayConfig(spec, d)

		if cfg.Attempt != spec.Ref.String() {
			t.Errorf("attempt = %q, want %q", cfg.Attempt, spec.Ref.String())
		}
		if cfg.TokenSHA256 != "" {
			t.Error("a materialized gateway authenticates nobody, so it must carry no token digest")
		}
		if diff := cmp.Diff(egress.Provider{}, cfg.Provider); diff != "" {
			t.Errorf("a materialized gateway must hold no provider credential (-want +got):\n%s", diff)
		}
		wantAllow(t, cfg.ConnectAllow,
			[]string{"api.anthropic.com", "claude.ai", "platform.claude.com", "proxy.corp.internal"})
	})

	t.Run("brokered keeps the token-authenticated header injection", func(t *testing.T) {
		d, err := driverFor("codex")
		if err != nil {
			t.Fatal(err)
		}
		spec := RunSpec{
			Ref:                testRef(),
			Custody:            custodyBrokered,
			Harness:            HarnessSpec{Kind: "codex", ProviderEndpoint: "https://api.openai.com"},
			BrokerToken:        "cbt1.abc",
			BrokerTokenSHA:     strings.Repeat("a", 64),
			ProviderCredential: "sk-openai",
		}
		cfg := r.gatewayConfig(spec, d)

		if cfg.TokenSHA256 != spec.BrokerTokenSHA {
			t.Errorf("token digest = %q, want %q", cfg.TokenSHA256, spec.BrokerTokenSHA)
		}
		want := egress.Provider{
			Endpoint:         "https://api.openai.com",
			Credential:       "sk-openai",
			CredentialHeader: "authorization",
			CredentialPrefix: "Bearer ",
			Ops: []egress.Op{
				{Method: "POST", PathPrefix: "/v1/responses"},
				{Method: "POST", PathPrefix: "/v1/chat/completions"},
				{Method: "GET", PathPrefix: "/v1/models"},
			},
		}
		if diff := cmp.Diff(want, cfg.Provider); diff != "" {
			t.Errorf("provider mismatch (-want +got):\n%s", diff)
		}
		wantAllow(t, cfg.ConnectAllow,
			[]string{"chatgpt.com", "auth.openai.com", "api.openai.com", "proxy.corp.internal"})
	})

	t.Run("the operator's list never leaks between attempts", func(t *testing.T) {
		d, err := driverFor("claude-code")
		if err != nil {
			t.Fatal(err)
		}
		before := slices.Clone(d.connectAllow)
		r.gatewayConfig(RunSpec{Ref: testRef(), Custody: custodyMaterialized}, d)
		r.gatewayConfig(RunSpec{Ref: testRef(), Custody: custodyMaterialized}, d)
		if diff := cmp.Diff(before, d.connectAllow); diff != "" {
			t.Errorf("the driver's pinned list was mutated (-before +after):\n%s", diff)
		}
	})
}

func wantAllow(t *testing.T, got, want []string) {
	t.Helper()
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	if diff := cmp.Diff(w, g); diff != "" {
		t.Errorf("connect allowlist mismatch (-want +got):\n%s", diff)
	}
}

// Every token an auth.json carries is a secret the redaction scan must know
// about; the fields that happen to be empty are not.
func TestCodexSensitive(t *testing.T) {
	file := codexFile(t, map[string]any{
		"tokens": map[string]any{
			"id_token": "id.tok", "access_token": "acc.tok",
			"refresh_token": "ref.tok", "account_id": "acct-1",
		},
		"OPENAI_API_KEY": "sk-openai",
	})
	got, err := codexSensitive(file)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"id.tok", "acc.tok", "ref.tok", "sk-openai"}
	if diff := cmp.Diff(want, asStrings(got)); diff != "" {
		t.Errorf("sensitive fields mismatch (-want +got):\n%s", diff)
	}
	if slices.Contains(asStrings(got), "acct-1") {
		t.Error("the account id is an identifier, not a secret")
	}

	sparse, err := codexSensitive(codexFile(t, map[string]any{
		"tokens": map[string]any{"refresh_token": "ref.tok"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"ref.tok"}, asStrings(sparse)); diff != "" {
		t.Errorf("empty fields must be dropped (-want +got):\n%s", diff)
	}

	if _, err := codexSensitive([]byte("not json")); err == nil {
		t.Fatal("an unparseable auth file yields no known secrets, so it must be an error")
	}
}

// The identity is a fingerprint the later sync-back compares a file against
// itself with: same account in, same digest out; a different account, or a
// swapped-in file from another subscription, must not match.
func TestCodexIdentity(t *testing.T) {
	base := codexFile(t, map[string]any{
		"tokens": map[string]any{
			"id_token":   idToken(t, map[string]any{"sub": "user-1", "email": "a@example.com"}),
			"account_id": "acct-1", "access_token": "acc.tok", "refresh_token": "ref.tok",
		},
	})
	first, err := codexIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 {
		t.Fatalf("identity = %q, want a sha256 hex digest", first)
	}

	// A refresh rewrites the short-lived tokens; the identity must survive it.
	refreshed := codexFile(t, map[string]any{
		"tokens": map[string]any{
			"id_token":   idToken(t, map[string]any{"sub": "user-1", "email": "a@example.com"}),
			"account_id": "acct-1", "access_token": "acc.tok2", "refresh_token": "ref.tok2",
		},
	})
	if got, err := codexIdentity(refreshed); err != nil || got != first {
		t.Errorf("identity = %q (%v), want it unchanged across a refresh (%q)", got, err, first)
	}

	other := codexFile(t, map[string]any{
		"tokens": map[string]any{
			"id_token":   idToken(t, map[string]any{"sub": "user-2", "email": "b@example.com"}),
			"account_id": "acct-2",
		},
	})
	if got, _ := codexIdentity(other); got == first {
		t.Error("a different account must not fingerprint to the same identity")
	}

	// account_id alone is still an identity; nothing at all is not.
	if _, err := codexIdentity(codexFile(t, map[string]any{
		"tokens": map[string]any{"account_id": "acct-1"},
	})); err != nil {
		t.Errorf("an account id is usable identity material: %v", err)
	}
	if _, err := codexIdentity(codexFile(t, map[string]any{
		"tokens": map[string]any{"refresh_token": "ref.tok"},
	})); err == nil {
		t.Fatal("a file with no identity material at all must be refused, not fingerprinted empty")
	}
	if _, err := codexIdentity([]byte("not json")); err == nil {
		t.Fatal("an unparseable auth file has no identity")
	}
}

// The materialized-only extractors are never reached for a brokered binding —
// this is the four-cell rule the pairing table exists to enforce.
func TestDriver_MaterializedExtractorsAreCredentialSpecific(t *testing.T) {
	d, err := driverFor("codex")
	if err != nil {
		t.Fatal(err)
	}
	cust, err := d.custodyOf("model_key")
	if err != nil {
		t.Fatal(err)
	}
	if cust != custodyBrokered {
		t.Fatalf("custody = %q, want brokered", cust)
	}
	// A raw model key is not an auth.json: feeding it to the codex extractors
	// fails loudly, which is why every consumer gates on custody first.
	if _, err := codexIdentity([]byte("sk-openai")); err == nil {
		t.Error("a model key must not fingerprint as a codex identity")
	}
	if _, err := codexSensitive([]byte("sk-openai")); err == nil {
		t.Error("a model key must not parse as a codex auth file")
	}
	if d.authMount == nil || d.authMount.containerPath != "/caesar-auth" ||
		d.authMount.envVar != "CODEX_HOME" || d.authMount.file != "auth.json" {
		t.Errorf("codex auth mount = %+v, want the pinned /caesar-auth CODEX_HOME auth.json", d.authMount)
	}
	cc, err := driverFor("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if cc.authMount != nil {
		t.Error("claude-code delivers its credential in the env, so it mounts no auth dir")
	}
	if cc.identity != nil {
		t.Error("a static subscription token has no read-back, so it pins no identity")
	}
}

// Every row must be well-formed in the ways later stages depend on: a
// sensitive extractor (redaction has no per-harness fallback) and exit rules
// that can actually be applied. The rules themselves land in Task 7; this is
// the invariant they will have to satisfy.
func TestDriver_RowsAreWellFormed(t *testing.T) {
	for kind, d := range drivers {
		t.Run(kind, func(t *testing.T) {
			if d.sensitive == nil {
				t.Fatal("a driver with no sensitive-field extractor cannot be redacted")
			}
			for i, rule := range d.exitMap {
				if rule.pattern == nil || rule.code == "" {
					t.Errorf("exit rule %d = %+v, want a pattern and a code", i, rule)
				}
				if rule.class != runs.OutcomePermanent && rule.class != runs.OutcomeTransient {
					t.Errorf("exit rule %d has class %q, want a typed outcome class", i, rule.class)
				}
				if rule.resetEpochCapture && rule.pattern != nil && rule.pattern.NumSubexp() < 1 {
					t.Errorf("exit rule %d claims a reset epoch but its pattern captures nothing", i)
				}
			}
		})
	}
	// The claude-code extractor is the identity case: the whole secret is the
	// one string a log must never carry.
	cc, err := driverFor("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	got, err := cc.sensitive([]byte("sk-ant-oat01-x"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"sk-ant-oat01-x"}, asStrings(got)); diff != "" {
		t.Errorf("sensitive mismatch (-want +got):\n%s", diff)
	}
}

func codexFile(t *testing.T, v map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// idToken builds the one part of a JWT this code reads: the payload segment.
// The signature is never verified — the fingerprint is a comparison key, not
// an authentication decision — so the other segments are filler.
func idToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "hdr." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

func asStrings(b [][]byte) []string {
	out := make([]string, len(b))
	for i, v := range b {
		out[i] = string(v)
	}
	return out
}
