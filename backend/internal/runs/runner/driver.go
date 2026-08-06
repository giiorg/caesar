package runner

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/egress"
)

// custody is how the attempt's provider credential reaches the harness:
// brokered means only the egress gateway ever holds it and the agent presents
// an attempt token instead, materialized means caesar writes the secret where
// the CLI reads it and the gateway holds nothing. The distinction decides who
// may change the credential mid-attempt, so it is derived once — from the
// (harness kind, credential kind) pair — and then carried, never re-guessed.
type custody string

const (
	custodyMaterialized custody = "materialized"
	custodyBrokered     custody = "brokered"
)

// exitRule maps one recognizable ending of a harness process onto a typed
// outcome. The rows are ordered and the driver is the one place per-harness
// knowledge lives.
type exitRule struct {
	pattern           *regexp.Regexp
	code              string
	class             runs.OutcomeClass
	resetEpochCapture bool // capture group 1 is a unix epoch → Outcome.RetryAt
}

// epochGuard closes the right-hand side of an epoch capture. Without it
// `(\d{9,12})` is happy to take the first twelve digits of a THIRTEEN-digit
// millisecond timestamp and call the result a second — a reset in the year
// 7620. Nothing downstream would catch it: retryDelay takes a provider-stated
// reset verbatim (retry.go — the cap and the jitter deliberately cannot
// shorten it), and evaluateFailure then sees a not-before past the run's wall
// deadline and TERMINALIZES the run instead of parking it. Failing the row
// outright is the right answer: the tail still matches the plain cap rule
// underneath, which waits on our own bounded guess.
const epochGuard = `(?:\D|$)`

// authMount is where a materialized credential lands inside the container and
// the environment variable that points the CLI at it.
type authMount struct{ containerPath, envVar, file string } // "/caesar-auth", "CODEX_HOME", "auth.json"

// envInput is what the driver's env function has to work with. Exactly one of
// the two values is meaningful, and which one is decided by custody: a
// materialized attempt has no broker token, a brokered one never sees the
// secret.
type envInput struct {
	custody     custody
	secret      string // materialized only: the opened credential
	brokerToken string // brokered only
}

// brokeredProfile is the gateway's half of a brokered attempt: which header
// carries the provider credential and which operations it may be spent on.
// Anything unlisted is denied — this table is the whole surface an agent can
// reach through somebody else's account.
type brokeredProfile struct {
	header, prefix string
	ops            []egress.Op
}

// driver is the whole per-harness contract in one row (spec §3): custody
// derivation, env delivery, the FQDN allowlist, the broker profile, exit
// mapping, and the sensitive-field/identity extractors for its credential.
//
// sensitive, identity and authMount describe the harness's MATERIALIZED
// credential kind only. Every consumer gates on custodyOf first: a brokered
// binding never routes through them — its delivery is the broker profile, its
// redaction entry is the raw credential bytes, and it materializes nothing.
type driver struct {
	kind         string
	connectAllow []string
	custodyFor   map[string]custody // credential kind → custody mode
	brokered     brokeredProfile
	env          func(envInput) []string
	exitMap      []exitRule // ordered: mapExit takes the first match
	sensitive    func(secret []byte) ([][]byte, error)
	identity     func(secret []byte) (string, error) // nil = no identity pin (static kinds)
	authMount    *authMount                          // nil = env-delivered credential
}

var drivers = map[string]driver{
	"claude-code": {
		kind: "claude-code",
		// Verified 2026-08-05 against code.claude.com/docs/en/network-config:
		// api.anthropic.com is the API; claude.ai and platform.claude.com are
		// OAuth token exchange/refresh. Telemetry (Datadog intakes) and the
		// updater are deliberately absent — default-denied, and the env kill-
		// switches below stop the CLI from trying (spec §3).
		connectAllow: []string{"api.anthropic.com", "claude.ai", "platform.claude.com"},
		custodyFor: map[string]custody{
			"subscription_token": custodyMaterialized,
			"model_key":          custodyBrokered,
		},
		brokered: brokeredProfile{header: "x-api-key", ops: []egress.Op{
			{Method: "POST", PathPrefix: "/v1/messages"},
			{Method: "POST", PathPrefix: "/v1/messages/count_tokens"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}},
		env: func(in envInput) []string {
			if in.custody == custodyBrokered {
				return []string{"ANTHROPIC_BASE_URL=" + brokerURL, "ANTHROPIC_API_KEY=" + in.brokerToken}
			}
			return []string{
				"CLAUDE_CODE_OAUTH_TOKEN=" + in.secret,
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
				"DISABLE_AUTOUPDATER=1",
			}
		},
		// Recorded 2026-08-05 against Claude Code v2.1.222, except where a row
		// says otherwise (the Task 14 smoke run re-verifies against whatever
		// ships). The epoch-bearing cap marker comes first: when the CLI names
		// the moment the window reopens, the retry must wait for THAT time,
		// not for our own guess.
		exitMap: []exitRule{
			{
				pattern: regexp.MustCompile(`(?i)usage limit reached\|(\d{9,12})` + epochGuard),
				code:    runs.CodeRateLimited, class: runs.OutcomeTransient, resetEpochCapture: true,
			},
			{
				pattern: regexp.MustCompile(`(?i)(?:5-hour|usage) limit reached`),
				code:    runs.CodeRateLimited, class: runs.OutcomeTransient,
			},
			{
				// Every alternative is a completing phrase the CLI owns: a
				// reason it states about itself, followed by the slash command
				// only it can offer. The tail these run over is the AGENT'S OWN
				// STDOUT, and this is the one row whose match is PERMANENT and
				// writes to the operator's credential row (markAuthExpired,
				// collect.go), so a bare `Invalid API key` or a bare `Please
				// run /login` — phrases a model types in passing about a CLI it
				// knows well — cannot stand alone here. The asymmetry is what
				// decides it: missing a real auth death costs a retry (baked
				// decision 13's transient harness_error, and the next attempt
				// meets the same dead token), while a false match kills a live
				// run permanently and flags a working credential.
				//
				// RECORDED VERBATIM (2026-08-06, the pinned v2.1.223 in
				// caesar-harness-claude:dev): started uncredentialed the CLI
				// prints `Not logged in · Please run /login` — one line, `·` as
				// U+00B7, no escape sequences, exit 1. The separator is matched
				// as bounded non-word runes rather than as those bytes: the log
				// tail carries the CLI's output unmodified, so the U+00B7 does
				// arrive intact, but a version that swaps it for `-` or `:`
				// must not silently stop classifying.
				//
				// NARROWED ON REASONING (not recorded in this shape): the
				// `invalid api key` and `oauth token …` reasons are v2.1.222
				// corpus phrases refitted onto the renderer shape above.
				pattern: regexp.MustCompile(
					`(?im)^(?:not logged in|invalid api key|oauth token (?:revoked|expired))\W{1,8}please run /login`),
				code: runs.CodeAuthExpired, class: runs.OutcomePermanent,
			},
		},
		sensitive: func(secret []byte) ([][]byte, error) { return [][]byte{secret}, nil },
		// identity nil: a static token has no read-back and no identity pin.
	},
	"codex": {
		kind: "codex",
		// Verified 2026-08-05 against the Codex CLI source: chatgpt.com is the
		// ChatGPT-auth backend (WebSocket upgrades land there too);
		// auth.openai.com is token refresh — without it a long run dies
		// opaquely mid-refresh (spec §3); api.openai.com covers apiKey-mode
		// auth.json files. ab.chatgpt.com (Statsig telemetry) deliberately absent.
		connectAllow: []string{"chatgpt.com", "auth.openai.com", "api.openai.com"},
		custodyFor: map[string]custody{
			"agent_auth_file": custodyMaterialized,
			"model_key":       custodyBrokered,
		},
		brokered: brokeredProfile{header: "authorization", prefix: "Bearer ", ops: []egress.Op{
			{Method: "POST", PathPrefix: "/v1/responses"},
			{Method: "POST", PathPrefix: "/v1/chat/completions"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}},
		env: func(in envInput) []string {
			if in.custody == custodyBrokered {
				return []string{"OPENAI_BASE_URL=" + brokerURL + "/v1", "OPENAI_API_KEY=" + in.brokerToken}
			}
			return []string{"CODEX_HOME=/caesar-auth"}
		},
		// PROVISIONAL: no recorded corpus for this CLI yet. Task 13's fakecli
		// speaks these exact markers, and the Task 14 smoke run replaces them
		// with recorded ones. The epoch row leads for the same reason it does
		// under claude-code: a stated reset outranks a guessed delay.
		//
		// The tail these run over is the AGENT'S OWN STDOUT, so every row has
		// to be a phrase the CLI states about itself rather than one an agent
		// could type in passing: "rate limit" alone matches an agent
		// discussing rate limits, and an agent that merely suggests running
		// `codex login` would have its live credential stamped dead.
		exitMap: []exitRule{
			{
				pattern: regexp.MustCompile(`(?i)usage limit reached\|(\d{9,12})` + epochGuard),
				code:    runs.CodeRateLimited, class: runs.OutcomeTransient, resetEpochCapture: true,
			},
			{
				pattern: regexp.MustCompile(`(?i)(?:usage|rate) limit (?:reached|exceeded)`),
				code:    runs.CodeRateLimited, class: runs.OutcomeTransient,
			},
			{
				// PROVISIONAL like its siblings, and narrowed ON REASONING —
				// there is nothing recorded to narrow onto. The rule three
				// paragraphs up is the specification, and the row this replaces
				// broke it: `(?i)token .*(?:expired|invalid)` matched an agent
				// writing "the token .* invalid case is handled below", and
				// that match is permanent and stamps the credential.
				//
				// Each alternative now has to OPEN a line, which is where a CLI
				// states an error about itself and where prose almost never
				// puts one; `invalid` is gone outright, because no phrasing of
				// it is a phrase the CLI owns. The optional preamble is codex's
				// own observed error shape — running v0.146.1 with no route out
				// (2026-08-06) yields both `ERROR: <msg>` and
				// `<ts> ERROR <module>: <msg>` — and it is gated on the literal
				// `error` so it cannot degrade into a free-floating prefix.
				pattern: regexp.MustCompile(
					`(?im)^(?:(?:\S+ +)?error\b[^\n]{0,60}?: +)?(?:401 unauthorized|(?:access|refresh|id) token (?:has )?expired)\b`),
				code: runs.CodeAuthExpired, class: runs.OutcomePermanent,
			},
		},
		sensitive: codexSensitive,
		identity:  codexIdentity,
		authMount: &authMount{containerPath: "/caesar-auth", envVar: "CODEX_HOME", file: "auth.json"},
	},
}

// driverFor resolves the contract for a harness kind. An unlisted kind has no
// allowlist, no custody rule and no broker profile, so it is refused rather
// than defaulted — a default here would be an open proxy onto a provider
// account. Callers wrap this as a permanent harness error.
func driverFor(kind string) (driver, error) {
	d, ok := drivers[kind]
	if !ok {
		return driver{}, fmt.Errorf("runner: unknown harness kind %q", kind)
	}
	return d, nil
}

// mapExit reads meaning into a nonzero exit the only way a harness offers it:
// recorded output markers over the log tail (spec §4). Rules are ordered —
// first match wins — and anything unmatched stays a transient harness_error,
// exactly what collect did before drivers existed. Exit 0 never arrives here:
// the caller gates on it.
func mapExit(d driver, exitCode int64, tail []byte) runs.Outcome {
	for _, rule := range d.exitMap {
		m := rule.pattern.FindSubmatch(tail)
		if m == nil {
			continue
		}
		out := runs.Outcome{
			Class: rule.class, Code: rule.code,
			Message: fmt.Sprintf("harness exited %d: %s", exitCode, rule.pattern.String()),
		}
		if rule.resetEpochCapture && len(m) > 1 {
			if sec, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil {
				at := time.Unix(sec, 0).UTC()
				out.RetryAt = &at // sanity clamping is retryDelay's job (spec §4.1)
			}
		}
		return out
	}
	return runs.Outcome{
		Class: runs.OutcomeTransient, Code: runs.CodeHarnessError,
		Message: fmt.Sprintf("harness exited %d", exitCode),
	}
}

// materializesAuthFile reports whether this pairing puts the credential on the
// host as a file the container reads: the harness has to pin a mount for one,
// and the custody has to be materialized. Both halves matter — the same driver
// brokering a model key materializes nothing, and a raw API key is not an
// auth.json. Every consumer asks this one question rather than re-deriving it,
// so the manifest's auth_dir, the identity pin and the container's bind can
// never disagree about whether a secret is on disk.
func (d driver) materializesAuthFile(cust custody) bool {
	return cust == custodyMaterialized && d.authMount != nil
}

// custodyOf says how this harness may hold that credential kind. A pairing
// outside the table produces a container that cannot authenticate at all — an
// auth.json means nothing to Claude Code — so it is refused here, where the
// failure is still cheap, rather than defaulted into one custody or the other.
func (d driver) custodyOf(credentialKind string) (custody, error) {
	c, ok := d.custodyFor[credentialKind]
	if !ok {
		return "", fmt.Errorf("runner: harness %s cannot use credential kind %q", d.kind, credentialKind)
	}
	return c, nil
}

// codexAuthFile is the runner's pinned subset of Codex's auth.json. Unknown
// fields are tolerated; the CLI owns the schema (spec §2). This mirrors the
// assets validator by value rather than by import: the two contexts never
// share code, and each reads the file for its own reason.
type codexAuthFile struct {
	Tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	OpenAIAPIKey string `json:"OPENAI_API_KEY"`
}

func parseCodexAuthFile(secret []byte) (codexAuthFile, error) {
	var f codexAuthFile
	if err := json.Unmarshal(secret, &f); err != nil {
		return codexAuthFile{}, fmt.Errorf("runner: parse codex auth file: %w", err)
	}
	return f, nil
}

// codexSensitive returns the file's token fields — the values the redaction
// scan must know (spec §3): id/access/refresh tokens and the API key, empties
// dropped. The account id is deliberately absent: it identifies, it does not
// authenticate, and redacting it would blank the very field an operator reads
// to tell two subscriptions apart.
func codexSensitive(secret []byte) ([][]byte, error) {
	f, err := parseCodexAuthFile(secret)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, v := range []string{f.Tokens.IDToken, f.Tokens.AccessToken, f.Tokens.RefreshToken, f.OpenAIAPIKey} {
		if v != "" {
			out = append(out, []byte(v))
		}
	}
	return out, nil
}

// codexIdentity fingerprints the stable account identity (spec §2.5):
// sha256 hex over account_id + "|" + sub + "|" + email, the last two decoded
// from the id_token's payload segment base64url-unverified — this is a
// fingerprint for comparing a file against itself later, not authentication.
// No usable identity material at all is an error (the caller treats the file
// as unscannable).
func codexIdentity(secret []byte) (string, error) {
	f, err := parseCodexAuthFile(secret)
	if err != nil {
		return "", err
	}
	sub, email := idTokenClaims(f.Tokens.IDToken)
	if f.Tokens.AccountID == "" && sub == "" && email == "" {
		return "", errors.New("runner: codex auth file carries no account identity")
	}
	sum := sha256.Sum256([]byte(f.Tokens.AccountID + "|" + sub + "|" + email))
	return hex.EncodeToString(sum[:]), nil
}

// idTokenClaims reads the two identity claims out of a JWT's payload segment.
// The signature is never checked and no claim is trusted as an authorization
// decision: the output is only ever hashed into a comparison key, so a
// malformed or absent token degrades to empty claims rather than an error.
func idTokenClaims(idToken string) (sub, email string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", ""
	}
	return claims.Sub, claims.Email
}
