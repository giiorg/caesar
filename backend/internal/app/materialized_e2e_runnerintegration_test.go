//go:build runnerintegration

package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// e2eFakeCLIImage is CI's agent: docker/fakecli.sh, built by `task image:build`
// as the Dockerfile's fakecli stage. No lane may ever place a live
// subscription call, so every driver behaviour CI proves is proven against it.
const e2eFakeCLIImage = "caesar-fakecli:dev"

// The credential this suite runs on, and the values docker/fakecli.sh writes
// over it. The refreshed pair is duplicated from that script on purpose: the
// script is a shell fixture with no way to export constants, and pinning them
// here means a divergence breaks a test rather than quietly weakening one.
// Every token clears the runner's 16-byte exact-match redaction floor.
const (
	matAccessToken  = "at-stored-0123456789abcdef"
	matRefreshToken = "rt-stored-0123456789abcdef"
	matAccountID    = "acct-materialized-e2e"

	matRefreshedAccess  = "at-refreshed-0123456789abcdef" // docker/fakecli.sh mutate_auth
	matRefreshedRefresh = "rt-refreshed-0123456789abcdef" // docker/fakecli.sh mutate_auth
	matIntruderAccount  = "acct-intruder"                 // docker/fakecli.sh poison_auth
	matRedactedMarker   = "[caesar:redacted]"             // runner redact.go redactedMarker
)

// matStoredAuth is the operator-supplied auth.json: a real three-segment
// id_token so the account fingerprint exercises the claim decoding too, not
// only account_id.
func matStoredAuth(t *testing.T) string {
	t.Helper()
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-e2e","email":"e2e@example.com"}`))
	raw, err := json.Marshal(map[string]any{"tokens": map[string]string{
		"id_token":      "eyJhbGciOiJub25lIn0." + claims + ".signature",
		"access_token":  matAccessToken,
		"refresh_token": matRefreshToken,
		"account_id":    matAccountID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// newMaterializedEnv builds a world whose agent is the fixture image running
// its OWN entrypoint — no scripted command — so the phase's instructions reach
// it exactly the way they reach a real CLI: through the staging contract at
// /workspace/.caesar/in/instructions.md.
func newMaterializedEnv(t *testing.T, engine *dockerapi.Client, repoDir, headSHA, name, storedAuth, instructions string) *runnerEnv {
	t.Helper()
	return newRunnerEnv(t, engine, envSpec{
		name: name, repoDir: repoDir, headSHA: headSHA,
		storedAuth: storedAuth, imageRef: e2eFakeCLIImage, instructions: instructions,
	})
}

// matDirectives joins one instruction script. The prose first line is not
// decoration: it proves the fixture ignores everything that is not a
// directive, which is what lets a real workflow's instructions carry both.
func matDirectives(lines ...string) string {
	return strings.Join(append([]string{"Implement the change."}, lines...), "\n")
}

// matWaitTerminal waits for any terminal status and reports the outcome when
// it is not the one the case wanted — a bare "timed out waiting for succeeded"
// says nothing about which of five ways the pipeline broke.
func matWaitTerminal(t *testing.T, e *runnerEnv, runID uuid.UUID) runs.Run {
	t.Helper()
	var got runs.Run
	lifeWaitFor(t, "run "+runID.String()+" to reach a terminal status", func() bool {
		run, _, _, err := e.runs.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		got = run
		switch run.Status {
		case runs.RunSucceeded, runs.RunFailed, runs.RunDead:
			return true
		}
		return false
	})
	return got
}

func matRequireStatus(t *testing.T, run runs.Run, want runs.RunStatus) {
	t.Helper()
	if run.Status != want {
		t.Fatalf("run status = %s, want %s (outcome %+v)", run.Status, want, run.Outcome)
	}
}

// matStoredSecret reads the credential back through the same audited path the
// runner uses, so the assertion is over what an attempt would actually be
// handed rather than over a row's bytes.
func matStoredSecret(t *testing.T, e *runnerEnv) ([]byte, int64) {
	t.Helper()
	secret, version, err := e.assets.UseCredentialVersioned(context.Background(), e.credID, "test:read-back")
	if err != nil {
		t.Fatalf("read the stored credential back: %v", err)
	}
	return secret, version
}

// matPublishedLog reads one of the attempt's PUBLISHED streams — the redacted
// copy publishLogs renamed into place, never the quarantined original.
func matPublishedLog(t *testing.T, e *runnerEnv, ref runs.AttemptRef, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.dataRoot, "logs", ref.String(), name))
	if err != nil {
		t.Fatalf("published %s for %s: %v", name, ref, err)
	}
	return string(raw)
}

// matPatchDiff decodes the run's patch artifact back to the canonical bytes.
// Nothing asserts against the base64 wire form: a redaction scan over the
// encoding could never match, and neither can a test's.
func matPatchDiff(t *testing.T, e *runnerEnv, runID uuid.UUID) string {
	t.Helper()
	payload := e2eArtifactPayload(t, e.pool, runID, runs.ArtifactKindPatch)
	encoded, ok := payload["diff"].(string)
	if !ok {
		t.Fatalf("patch payload carries no diff string: %v", payload)
	}
	diff, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("patch diff is not base64: %v", err)
	}
	return string(diff)
}

// matGatewayStart finds the gateway's startup line in its published log. It is
// the only place the attempt's egress posture is observable after teardown.
func matGatewayStart(t *testing.T, e *runnerEnv, ref runs.AttemptRef) map[string]any {
	t.Helper()
	for line := range strings.SplitSeq(matPublishedLog(t, e, ref, "gateway.log"), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &entry); err != nil {
			continue
		}
		if entry["msg"] == "egress.start" {
			return entry
		}
	}
	t.Fatalf("no egress.start line in the gateway log for %s", ref)
	return nil
}

// The materialized-custody proof (spec §6), and the CI-provable half of it:
// the operator's own subscription credential travelling through the whole
// stack to a real container and back, with a scripted CLI standing in for the
// one thing CI may never do.
func TestMaterializedPipeline_TheWholeStackAgainstAScriptedCLI(t *testing.T) {
	if _, err := os.Stat(e2eDockerSocket); err != nil {
		t.Skipf("the materialized pipeline suite needs a Docker daemon at %s: %v", e2eDockerSocket, err)
	}
	engine := dockerapi.New(e2eDockerSocket)
	for _, ref := range []string{e2eCaesarImage, e2eGitImage, e2eFakeCLIImage} {
		if _, err := engine.ImageInspect(context.Background(), ref); err != nil {
			t.Skipf("image %s is missing (run `task image:build`): %v", ref, err)
		}
	}
	repoDir := t.TempDir()
	headSHA, err := fake.InitBareRepo(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	// 1. The pipeline end to end: the credential reaches the CLI as a file it
	//    can read AND rewrite, its refresh lands in the store, everything the
	//    agent printed of it is redacted before publication, and the gateway
	//    holding no credential opens the provider's own hosts instead.
	t.Run("the credential is delivered as a file, refreshed, synced back and redacted", func(t *testing.T) {
		stored := matStoredAuth(t)
		e := newMaterializedEnv(t, engine, repoDir, headSHA, "materialized-pipeline", stored, matDirectives(
			"fakecli:assert-no-broker",
			"fakecli:print-auth-sha",
			"fakecli:mutate-auth",
			"fakecli:print-secret",
			"fakecli:write out.txt done",
			"fakecli:exit 0",
		))
		e.startOrchestrator(t, e.rnr, 1)
		run := e.newRun(t, "materialized pipeline")
		ref := runs.AttemptRef{RunID: run.ID, NodeID: "impl", Attempt: 1}
		matRequireStatus(t, matWaitTerminal(t, e, run.ID), runs.RunSucceeded)

		// Delivery, both halves at once: CODEX_HOME pointed at the bind, and
		// the bytes behind it were the stored credential to the last byte.
		wantSHA := sha256.Sum256([]byte(stored))
		agentLog := matPublishedLog(t, e, ref, "agent.log")
		if want := "fakecli:auth-sha " + hex.EncodeToString(wantSHA[:]); !strings.Contains(agentLog, want) {
			t.Errorf("the agent did not read the stored credential at $CODEX_HOME; want %q in:\n%s", want, agentLog)
		}
		if !strings.Contains(agentLog, "fakecli:no-broker-token") {
			t.Errorf("the agent did not report a broker-free environment:\n%s", agentLog)
		}

		// The patch is the agent's work and nothing else: the credential lives
		// outside the one bind it shares with the control plane.
		diff := matPatchDiff(t, e, run.ID)
		for _, want := range []string{"out.txt", "auth-sha.txt", hex.EncodeToString(wantSHA[:])} {
			if !strings.Contains(diff, want) {
				t.Errorf("patch does not mention %q:\n%s", want, diff)
			}
		}
		for _, banned := range []string{"auth.json", matAccessToken, matRefreshToken, matRefreshedAccess, matRefreshedRefresh} {
			if strings.Contains(diff, banned) {
				t.Errorf("patch carries %q, which never belongs in a workspace:\n%s", banned, diff)
			}
		}

		// The refresh the CLI performed is in the store, at the next version,
		// with a receipt naming the attempt that put it there.
		secret, version := matStoredSecret(t, e)
		if version != 2 {
			t.Errorf("credential secret_version = %d, want 2 — the CLI's refresh landed exactly once", version)
		}
		if !strings.Contains(string(secret), matRefreshedRefresh) {
			t.Errorf("the stored credential is not the refreshed one: %s", secret)
		}
		var epoch, from, to int64
		if err := e.pool.QueryRow(context.Background(),
			`SELECT epoch, from_version, to_version FROM credential_sync_receipts
			 WHERE credential_id = $1 AND attempt_ref = $2`, e.credID, ref.String()).Scan(&epoch, &from, &to); err != nil {
			t.Fatalf("sync receipt for %s: %v", ref, err)
		}
		if epoch != 1 || from != 1 || to != 2 {
			t.Errorf("sync receipt = epoch %d, %d->%d; want epoch 1, 1->2", epoch, from, to)
		}

		// Redaction over the exact value the CLI printed — which, because
		// print-secret ran after the refresh, is a token caesar did not know
		// until the read-back reported it (spec §3).
		if !strings.Contains(agentLog, "fakecli:secret "+matRedactedMarker) {
			t.Errorf("the printed access token was not redacted:\n%s", agentLog)
		}
		for _, banned := range []string{matAccessToken, matRefreshToken, matRefreshedAccess, matRefreshedRefresh} {
			if strings.Contains(agentLog, banned) {
				t.Errorf("the published agent log carries %q:\n%s", banned, agentLog)
			}
		}

		// Open CONNECT: a materialized gateway brokers nothing, so it holds no
		// credential to authenticate access to — and the allowlist is then the
		// sole barrier, which is why it must carry the driver's own FQDNs.
		start := matGatewayStart(t, e, ref)
		allow := fmt.Sprint(start["connect_allow"])
		for _, host := range []string{"chatgpt.com", "auth.openai.com", "api.openai.com"} {
			if !strings.Contains(allow, host) {
				t.Errorf("gateway connect_allow = %v, missing the codex host %s", allow, host)
			}
		}
		if start["broker"] != false {
			t.Errorf("gateway started with broker = %v, want false: a materialized attempt has no token to hold", start["broker"])
		}
		if ops, ok := start["ops"].(float64); !ok || ops != 0 {
			t.Errorf("gateway started with ops = %v, want the key present and 0", start["ops"])
		}
	})

	// 2. The cap hit: the CLI names when the window reopens, the retry waits
	//    for THAT moment, and the attempt that wakes up meets the credential
	//    its predecessor refreshed. The cross-attempt state is entirely in the
	//    credential store — nothing in this process remembers anything.
	t.Run("a cap wait honours the stated reset and resumes on the refreshed credential", func(t *testing.T) {
		resetAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		e := newMaterializedEnv(t, engine, repoDir, headSHA, "cap-wait", matStoredAuth(t), matDirectives(
			"fakecli:rate-limit-until-refreshed "+strconv.FormatInt(resetAt.Unix(), 10),
			"fakecli:write out.txt done",
		))
		e.startOrchestrator(t, e.rnr, 1)
		run := e.newRun(t, "cap wait and resume")

		// Attempt 1 hit the cap, and the outcome carries the provider's own
		// reset rather than one of our guesses.
		var rawOutcome []byte
		lifeWaitFor(t, "attempt 1 to fail with a cap outcome", func() bool {
			err := e.pool.QueryRow(context.Background(),
				`SELECT outcome FROM phase_runs WHERE run_id = $1 AND node_id = 'impl' AND attempt = 1
				   AND status = 'failed' AND outcome IS NOT NULL`, run.ID).Scan(&rawOutcome)
			return err == nil
		})
		var out struct {
			Code    string `json:"code"`
			RetryAt string `json:"retry_at"`
		}
		if err := json.Unmarshal(rawOutcome, &out); err != nil {
			t.Fatal(err)
		}
		if out.Code != runs.CodeRateLimited {
			t.Fatalf("attempt 1 outcome code = %q, want %s", out.Code, runs.CodeRateLimited)
		}
		gotReset, err := time.Parse(time.RFC3339, out.RetryAt)
		if err != nil {
			t.Fatalf("outcome retry_at %q is not a timestamp: %v", out.RetryAt, err)
		}
		if !gotReset.Equal(resetAt) {
			t.Errorf("outcome retry_at = %s, want the epoch the marker stated (%s)", gotReset, resetAt)
		}

		// The successor row is parked on that reset and cost the node nothing.
		var parkedID uuid.UUID
		var notBefore time.Time
		var charged int32
		lifeWaitFor(t, "the retry row to be parked", func() bool {
			return e.pool.QueryRow(context.Background(),
				`SELECT id, not_before, chargeable_failures FROM phase_runs
				 WHERE run_id = $1 AND node_id = 'impl' AND attempt = 2 AND not_before IS NOT NULL`,
				run.ID).Scan(&parkedID, &notBefore, &charged) == nil
		})
		if charged != 0 {
			t.Errorf("chargeable_failures = %d after a cap wait, want 0", charged)
		}
		if delta := notBefore.Sub(resetAt); delta < -time.Minute || delta > time.Minute {
			t.Errorf("not_before = %s, want the stated reset %s (off by %s)", notBefore, resetAt, delta)
		}

		// The wait is real time we refuse to spend (the Plan 05 precedent).
		if _, err := e.pool.Exec(context.Background(),
			`UPDATE phase_runs SET not_before = now() - interval '1 second' WHERE id = $1`, parkedID); err != nil {
			t.Fatal(err)
		}
		matRequireStatus(t, matWaitTerminal(t, e, run.ID), runs.RunSucceeded)

		if diff := matPatchDiff(t, e, run.ID); !strings.Contains(diff, "out.txt") {
			t.Errorf("the resumed attempt produced no out.txt:\n%s", diff)
		}
		// Attempt 2 took the already-refreshed branch, which it could only
		// reach by being handed the credential attempt 1 committed.
		attempt2 := runs.AttemptRef{RunID: run.ID, NodeID: "impl", Attempt: 2}
		if log := matPublishedLog(t, e, attempt2, "agent.log"); !strings.Contains(log, "fakecli:credential-already-refreshed") {
			t.Errorf("attempt 2 did not see the refreshed credential:\n%s", log)
		}
		if _, version := matStoredSecret(t, e); version != 2 {
			t.Errorf("credential secret_version = %d after the resume, want 2 — attempt 2 refreshed nothing", version)
		}
		var maxCharged int32
		if err := e.pool.QueryRow(context.Background(),
			`SELECT max(chargeable_failures) FROM phase_runs WHERE run_id = $1`, run.ID).Scan(&maxCharged); err != nil {
			t.Fatal(err)
		}
		if maxCharged != 0 {
			t.Errorf("chargeable_failures reached %d, want 0 — a cap wait is not a failure", maxCharged)
		}
	})

	// 3. A dead credential is an operator's problem, not a retry's: fail fast,
	//    name the credential in the message, and stamp it so the next claimant
	//    does not spend an attempt rediscovering it (spec §4, §5).
	t.Run("auth death fails fast, names the credential and stamps it dead", func(t *testing.T) {
		e := newMaterializedEnv(t, engine, repoDir, headSHA, "auth-death", matStoredAuth(t),
			matDirectives("fakecli:auth-death"))
		e.startOrchestrator(t, e.rnr, 1)
		run := e.newRun(t, "auth death")
		final := matWaitTerminal(t, e, run.ID)
		matRequireStatus(t, final, runs.RunFailed)

		if final.Outcome == nil || final.Outcome.Code != runs.CodeAuthExpired {
			t.Fatalf("run outcome = %+v, want %s", final.Outcome, runs.CodeAuthExpired)
		}
		if !strings.Contains(final.Outcome.Message, e.credID.String()) {
			t.Errorf("outcome message %q does not name the credential %s an operator has to fix",
				final.Outcome.Message, e.credID)
		}
		if !lifeScalar[bool](t, e.pool,
			`SELECT last_auth_expired_at IS NOT NULL FROM credentials WHERE id = $1`, e.credID) {
			t.Error("last_auth_expired_at is unset: the next claimant would spend an attempt rediscovering this")
		}
		if n := lifeScalar[int64](t, e.pool,
			`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND node_id = 'impl'`, run.ID); n != 1 {
			t.Errorf("%d attempts against a dead credential, want 1 — auth_expired is permanent", n)
		}
	})

	// 4. A read-back that is somebody else's subscription is content the agent
	//    authored, not a refresh (spec §2.5): the store is preserved untouched
	//    and the substitution is on the record.
	t.Run("a poisoned identity quarantines and preserves the stored credential", func(t *testing.T) {
		stored := matStoredAuth(t)
		e := newMaterializedEnv(t, engine, repoDir, headSHA, "poisoned-identity", stored,
			matDirectives("fakecli:poison-auth", "fakecli:exit 0"))
		e.startOrchestrator(t, e.rnr, 1)
		run := e.newRun(t, "poisoned identity")
		final := matWaitTerminal(t, e, run.ID)
		matRequireStatus(t, final, runs.RunFailed)

		if final.Outcome == nil || final.Outcome.Code != runs.CodeCredentialQuarantined {
			t.Fatalf("run outcome = %+v, want %s", final.Outcome, runs.CodeCredentialQuarantined)
		}
		secret, version := matStoredSecret(t, e)
		if version != 1 || string(secret) != stored {
			t.Errorf("the stored credential moved to version %d (%s); the operator's own bytes must survive intact",
				version, secret)
		}
		if strings.Contains(string(secret), matIntruderAccount) {
			t.Error("the agent's substituted account reached the store")
		}
		if n := lifeScalar[int64](t, e.pool,
			`SELECT count(*) FROM audit_entries WHERE action = 'credential.sync_quarantined' AND subject_id = $1`,
			e.credID.String()); n != 1 {
			t.Errorf("%d credential.sync_quarantined audit rows, want 1", n)
		}
	})

	// 5. The torn write (spec §2.7): nobody can say what the live credential
	//    is any more, so no complete redaction scan exists — and everything the
	//    agent could have written into is withheld, permanently, with the
	//    unscanned bytes kept as evidence.
	t.Run("a torn read-back withholds every workspace-derived artifact", func(t *testing.T) {
		stored := matStoredAuth(t)
		e := newMaterializedEnv(t, engine, repoDir, headSHA, "torn-readback", stored,
			matDirectives("fakecli:garble-auth", "fakecli:write out.txt x", "fakecli:exit 0"))
		e.startOrchestrator(t, e.rnr, 1)
		run := e.newRun(t, "torn read-back")
		ref := runs.AttemptRef{RunID: run.ID, NodeID: "impl", Attempt: 1}
		final := matWaitTerminal(t, e, run.ID)
		matRequireStatus(t, final, runs.RunFailed)

		if final.Outcome == nil || final.Outcome.Code != runs.CodeInvalidOutput {
			t.Fatalf("run outcome = %+v, want %s", final.Outcome, runs.CodeInvalidOutput)
		}
		if n := lifeScalar[int64](t, e.pool,
			`SELECT count(*) FROM artifacts WHERE run_id = $1 AND type IN ($2, $3)`,
			run.ID, runs.ArtifactKindLogRef, runs.ArtifactKindPatch); n != 0 {
			t.Errorf("%d log_ref/patch artifacts survived a withheld attempt, want 0", n)
		}
		if _, err := os.Stat(filepath.Join(e.dataRoot, "logs", ref.String(), "agent.log")); !os.IsNotExist(err) {
			t.Errorf("the agent log was published despite an unscannable read-back (stat err = %v)", err)
		}
		if _, err := os.Stat(filepath.Join(e.dataRoot, "quarantine", ref.String(), "agent.log")); err != nil {
			t.Errorf("the quarantined stream was not retained as evidence: %v", err)
		}
		secret, version := matStoredSecret(t, e)
		if version != 1 || string(secret) != stored {
			t.Errorf("the stored credential moved to version %d (%s); a torn file may never be committed",
				version, secret)
		}
	})
}
