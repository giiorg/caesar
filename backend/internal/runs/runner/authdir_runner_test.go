//go:build runnerintegration

package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
// only copy of a refreshed token, and sync-back's whole subject (spec §2.4).
// The workspace bind is a separate world: the credential is not in it, so
// nothing the extraction pipeline produces can carry the file out.
func TestExecute_CodexAuthDirIsDeliveredWritableAndOutlivesTheContainer(t *testing.T) {
	script := `set -e
sha256sum "$CODEX_HOME/auth.json" | cut -d' ' -f1 > /workspace/delivered.sha256
cat > "$CODEX_HOME/auth.json" <<'EOF'
` + refreshedAuthJSON + `
EOF
`
	leaser := &fakeLeaser{lease: Lease{Epoch: 2, SecretVersion: 9}, secret: []byte(codexAuthJSON)}
	r := newDockerRunner(t, Options{}, scriptedCodexHarness(script), &recordingSink{}, leaser)
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

	// Survival: teardown removed the attempt tree; the refreshed credential is
	// the one thing left, and Task 9 reads exactly this file.
	if _, err := os.Stat(r.attemptDir(ref)); !os.IsNotExist(err) {
		t.Errorf("attempt dir survived teardown, stat err = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(r.authDir(ref), "auth.json"))
	if err != nil {
		t.Fatalf("the refreshed credential did not survive the container: %v", err)
	}
	if strings.TrimSpace(string(raw)) != refreshedAuthJSON {
		t.Errorf("host-side auth.json = %q, want the agent's own rewrite", raw)
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
