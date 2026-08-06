//go:build runnerintegration

package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// The credential the materialized cases run on: a real-shaped auth.json whose
// tokens clear the redaction floor, so a hit proves the scan rather than the
// fixture.
const (
	dockerAccessToken  = "at-9f3c1d7e2b408a56c9d1e0f4b7a2c8d5"
	dockerRefreshToken = "rt-2e8b4a1c0d97f356e2a8c1b40d7f9e63"
	dockerAuthJSON     = `{"tokens":{"access_token":"` + dockerAccessToken +
		`","refresh_token":"` + dockerRefreshToken + `","account_id":"acct-docker"}}`
)

// codexScriptedHarness runs a shell script as a codex-kind agent: the harness
// whose credential is materialized as a file the script can read, which is what
// makes an exfiltration case realistic rather than staged.
func codexScriptedHarness(script string) HarnessSpec {
	return HarnessSpec{
		Kind:       "codex",
		ImageRef:   gitImage,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{script},
	}
}

func codexRequest(t *testing.T, ref runs.AttemptRef, baseSHA string) runs.ExecutionRequest {
	t.Helper()
	req := implementRequest(t, ref, baseSHA)
	req.Credential = runs.CredentialBinding{ID: uuid.New(), Kind: kindAgentAuthFile}
	return req
}

func codexLeaser() *fakeLeaser {
	return &fakeLeaser{lease: Lease{Epoch: 1, SecretVersion: 1}, secret: []byte(dockerAuthJSON)}
}

// The exfiltration case, end to end against a real daemon: the agent reads the
// credential it legitimately holds, prints it, and copies it into the
// workspace. The spec accepts that it CAN (§3, decision 7.4); what caesar
// promises is that the copy never becomes a durable artifact and the printed
// one never becomes a published log.
func TestExecute_ACredentialCopiedIntoTheWorkspaceFailsClosedAndIsRedactedFromTheLog(t *testing.T) {
	script := `set -e
TOKEN=$(sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p' "$CODEX_HOME/auth.json")
echo "authenticated with $TOKEN"
echo "$TOKEN" > /workspace/LEAK.md
`
	leaser := codexLeaser()
	r := newDockerRunner(t, Options{}, codexScriptedHarness(script), &recordingSink{}, leaser)
	ref := dockerRef()

	res, err := r.Execute(t.Context(), codexRequest(t, ref, fixtureSHA(t)))

	out := outcomeOf(t, err)
	if out.Class != runs.OutcomePolicyViolation || out.Code != runs.CodeSecretInArtifact {
		t.Fatalf("outcome = %s/%s, want policy_violation/%s\nagent log:\n%s",
			out.Class, out.Code, runs.CodeSecretInArtifact, readLogFile(t, r, ref, agentLogFile))
	}
	if _, ok := artifactTypes(res.Artifacts)[runs.ArtifactKindPatch]; ok {
		t.Errorf("the offending patch was persisted anyway: %v", res.Artifacts)
	}

	// The agent printed the token, so the quarantined stream carried it; only
	// the redacted copy was ever published.
	published := readLogFile(t, r, ref, agentLogFile)
	if strings.Contains(published, dockerAccessToken) {
		t.Errorf("the published agent log carries the access token:\n%s", published)
	}
	if !strings.Contains(published, redactedMarker) {
		t.Errorf("the published agent log carries no %s marker:\n%s", redactedMarker, published)
	}
	if _, err := os.Stat(r.quarantineDir(ref)); !os.IsNotExist(err) {
		t.Errorf("the quarantine survived a published attempt, stat err = %v", err)
	}
}

// Withholding suppresses an attempt's logs and artifacts, NEVER its typed
// outcome. The §2.7 window is exactly where the two collide: the CLI dies
// inside its own rewrite of auth.json while the harness is exiting on a cap
// window or a dead credential, and classifying that off an empty published
// directory would report a generic harness_error — charging a cap wait to the
// node and hiding a credential the operator has to replace.
func TestExecute_AnUnscannableReadBackStillTypesTheExit(t *testing.T) {
	reset := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	cases := []struct {
		name      string
		marker    string
		wantClass runs.OutcomeClass
		wantCode  string
		wantReset bool
	}{
		{
			name:   "an auth-death marker",
			marker: "401 Unauthorized",
			// The credential the operator has to replace, named — not a
			// transient the scheduler will retry against the same dead token.
			wantClass: runs.OutcomePermanent, wantCode: runs.CodeAuthExpired,
		},
		{
			name:      "a cap marker with its reset",
			marker:    fmt.Sprintf("usage limit reached|%d", reset.Unix()),
			wantClass: runs.OutcomeTransient, wantCode: runs.CodeRateLimited, wantReset: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The torn write is the whole point: what the CLI was in the middle
			// of persisting is unreadable, so the refreshed token is unknowable.
			script := fmt.Sprintf(`printf '{"tokens": {"acc' > "$CODEX_HOME/auth.json"
echo %q
echo work > /workspace/NEW.md
exit 1
`, tc.marker)
			leaser := codexLeaser()
			r := newDockerRunner(t, Options{}, codexScriptedHarness(script), &recordingSink{}, leaser)
			ref := dockerRef()

			res, err := r.Execute(t.Context(), codexRequest(t, ref, fixtureSHA(t)))

			out := outcomeOf(t, err)
			if out.Class != tc.wantClass || out.Code != tc.wantCode {
				t.Fatalf("outcome = %s/%s, want %s/%s — classified off the quarantined tail",
					out.Class, out.Code, tc.wantClass, tc.wantCode)
			}
			if tc.wantReset && (out.RetryAt == nil || !out.RetryAt.Equal(reset)) {
				t.Errorf("RetryAt = %v, want the marker's own %s", out.RetryAt, reset)
			}
			if commits := leaser.commitCalls(); len(commits) != 0 {
				t.Errorf("commits = %+v, want none: an unreadable file is never committed", commits)
			}

			// Withheld: nothing published, the evidence retained.
			if entries, rErr := os.ReadDir(r.logsDir(ref)); rErr == nil && len(entries) != 0 {
				t.Errorf("logs dir holds %v, want nothing published from an unscannable attempt", entries)
			} else if rErr != nil && !os.IsNotExist(rErr) {
				t.Fatal(rErr)
			}
			if _, sErr := os.Stat(filepath.Join(r.quarantineDir(ref), agentLogFile)); sErr != nil {
				t.Errorf("the quarantined log was not retained as evidence: %v", sErr)
			}
			byType := artifactTypes(res.Artifacts)
			if _, ok := byType[runs.ArtifactKindLogRef]; ok {
				t.Errorf("a withheld attempt pointed at logs that were never published: %v", res.Artifacts)
			}
			snap, ok := byType[runs.ArtifactKindFailureSnapshot]
			if !ok {
				t.Fatalf("no failure snapshot in %v", res.Artifacts)
			}
			for _, key := range []string{"log_tail", "workspace"} {
				if _, ok := snap.Payload[key]; ok {
					t.Errorf("the withheld snapshot carries agent-derived %q: %v", key, snap.Payload)
				}
			}
		})
	}
}
