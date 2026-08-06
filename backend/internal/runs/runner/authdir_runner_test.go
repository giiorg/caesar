//go:build runnerintegration

package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// refreshedAuthJSON is what the CLI leaves behind — the same account, newer
// tokens. It is the only one of the two files compared by value: what the
// container was GIVEN is proven by hash, so a real credential's bytes would
// never have to appear in a test.
const refreshedAuthJSON = `{"tokens":{"access_token":"at-2","refresh_token":"rt-2","account_id":"acct-1"}}`

// The materialized file path against a real daemon: the CLI finds its
// auth.json where the driver says it will, may rewrite it in place, and what
// it wrote survives the container it wrote it in — that surviving file is the
// only copy of a refreshed token, and the conclusion commits exactly those
// bytes back under the granted fence before anything is released or removed
// (spec §2.4). The workspace bind is a separate world: the credential is not
// in it, so nothing the extraction pipeline produces can carry the file out.
func TestExecute_CodexAuthDirIsDeliveredWritableAndOutlivesTheContainer(t *testing.T) {
	script := `set -e
sha256sum "$CODEX_HOME/auth.json" | cut -d' ' -f1 > /workspace/delivered.sha256
cat > "$CODEX_HOME/auth.json" <<'EOF'
` + refreshedAuthJSON + `
EOF
`
	leaser := &fakeLeaser{lease: Lease{Epoch: 2, SecretVersion: 9}, secret: []byte(codexAuthJSON)}
	// The fast heartbeat pins the stop-before-release ordering: the fake
	// answers conflict to any heartbeat on a not-held lease, so a heartbeat
	// goroutine still alive through the post-release extraction work would
	// fence, cancel the attempt, and fail this run.
	r := newDockerRunner(t, Options{testLeaseHeartbeatInterval: 50 * time.Millisecond},
		scriptedCodexHarness(script), &recordingSink{}, leaser)
	ref := dockerRef()
	req := codexAuthRequest(t, ref, fixtureSHA(t))

	res, err := r.Execute(t.Context(), req)
	if err != nil {
		t.Fatalf("execute: %v\nagent log:\n%s", err, readLogFile(t, r, ref, agentLogFile))
	}

	// Delivery: what the container hashed is what the lease granted.
	diff := decodedDiff(t, artifactsByType(res.Artifacts)[runs.ArtifactKindPatch].Payload)
	granted := sha256.Sum256([]byte(codexAuthJSON))
	if !strings.Contains(diff, hex.EncodeToString(granted[:])) {
		t.Errorf("the agent hashed something other than the granted credential:\n%s", diff)
	}

	// Isolation: the auth directory is outside the workspace bind, so neither
	// the file nor its contents can ride the patch out.
	if strings.Contains(diff, "auth.json") || strings.Contains(diff, "rt-1") {
		t.Errorf("the credential reached the patch:\n%s", diff)
	}

	// Sync-back: the file the container rewrote outlived it, and the
	// conclusion committed the mutated bytes — the only copy of the newest
	// tokens — gated on the granted epoch and version.
	commits := leaser.commitCalls()
	if len(commits) != 1 {
		t.Fatalf("CommitSync calls = %+v, want exactly one", commits)
	}
	if got := strings.TrimSpace(string(commits[0].secret)); got != refreshedAuthJSON {
		t.Errorf("committed %q, want the agent's own rewrite", got)
	}
	if commits[0].epoch != 2 || commits[0].expectVersion != 9 {
		t.Errorf("commit gated at epoch %d, version %d; want the granted 2/9",
			commits[0].epoch, commits[0].expectVersion)
	}

	// The obligation is over: teardown removed the attempt tree, and the
	// committed credential left the host with the manifest that described it.
	if _, err := os.Stat(r.attemptDir(ref)); !os.IsNotExist(err) {
		t.Errorf("attempt dir survived teardown, stat err = %v", err)
	}
	if _, err := os.Stat(r.authDir(ref)); !os.IsNotExist(err) {
		t.Errorf("auth dir survived its committed conclusion, stat err = %v", err)
	}
	if _, err := os.Stat(r.manifestPath(ref)); !os.IsNotExist(err) {
		t.Errorf("manifest survived its committed conclusion, stat err = %v", err)
	}

	// The mutable kind is single-flighted end to end: taken once for this
	// attempt, handed back once.
	acquired, _, released := leaser.calls()
	if len(acquired) != 1 || acquired[0].credentialID != req.Credential.ID {
		t.Errorf("acquire calls = %+v, want exactly one for the bound credential", acquired)
	}
	if len(released) != 1 || released[0].epoch != 2 {
		t.Errorf("release calls = %+v, want exactly one at the granted epoch", released)
	}
}

// A takeover mid-attempt — simulated by advancing the fake's epoch past the
// grant's — means a reconciler owns the credential now. The paused worker's
// conclusion commits nothing and deletes nothing: the refreshed file stays on
// disk as the reconciler's recovery evidence, the lease is never released,
// and the attempt reports the zero-charge wait rather than a success whose
// sync obligation is still pending.
func TestExecute_FencedSyncBackHoldsTheLeaseAndKeepsTheEvidence(t *testing.T) {
	script := `set -e
cat > "$CODEX_HOME/auth.json" <<'EOF'
` + refreshedAuthJSON + `
EOF
`
	leaser := &fakeLeaser{lease: Lease{Epoch: 2, SecretVersion: 9}, secret: []byte(codexAuthJSON), currentEpoch: 3}
	// The background heartbeat is pushed past the test's horizon: the fence
	// under test is the conclusion's own, not a mid-run cancellation.
	r := newDockerRunner(t, Options{testLeaseHeartbeatInterval: time.Hour},
		scriptedCodexHarness(script), &recordingSink{}, leaser)
	ref := dockerRef()
	req := codexAuthRequest(t, ref, fixtureSHA(t))

	_, err := r.Execute(t.Context(), req)

	out := outcomeOf(t, err)
	if out.Class != runs.OutcomeTransient || out.Code != runs.CodeCredentialBusy {
		t.Errorf("outcome = %s/%s, want transient/%s", out.Class, out.Code, runs.CodeCredentialBusy)
	}
	commits := leaser.commitCalls()
	if len(commits) != 1 || commits[0].epoch != 2 {
		t.Fatalf("CommitSync calls = %+v, want exactly one at the stale epoch 2", commits)
	}
	if _, _, released := leaser.calls(); len(released) != 0 {
		t.Errorf("release calls = %+v, want none: the reconciler owns the lease now", released)
	}
	raw, err := os.ReadFile(filepath.Join(r.authDir(ref), "auth.json"))
	if err != nil {
		t.Fatalf("the fenced worker destroyed the recovery file: %v", err)
	}
	if strings.TrimSpace(string(raw)) != refreshedAuthJSON {
		t.Errorf("host-side auth.json = %q, want the agent's rewrite left in place", raw)
	}
	if _, err := os.Stat(r.manifestPath(ref)); err != nil {
		t.Errorf("the fenced worker destroyed the manifest: %v", err)
	}
}

// scriptedCodexHarness runs the suite's shell-script agent under the Codex
// driver — the harness whose credential is a file rather than an env var.
func scriptedCodexHarness(script string) HarnessSpec {
	hs := scriptedHarness("", script)
	hs.Kind = "codex"
	return hs
}

// codexAuthRequest binds the one credential kind a running attempt can itself
// replace.
func codexAuthRequest(t *testing.T, ref runs.AttemptRef, baseSHA string) runs.ExecutionRequest {
	t.Helper()
	req := implementRequest(t, ref, baseSHA)
	req.Credential = runs.CredentialBinding{ID: uuid.New(), Kind: kindAgentAuthFile}
	return req
}
