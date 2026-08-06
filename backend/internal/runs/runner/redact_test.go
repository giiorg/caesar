package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// The fixtures are shaped like the values they stand in for and long enough to
// clear the floor: a test whose "secret" is four bytes would prove the scan
// works only on values minSecretLen drops on the floor.
const (
	claudeOAuthToken = "sk-ant-oat01-3PZk9vQmR7bTx2Lw8NcYdFgHjKpS"
	codexAccessToken = "eyJhbGciOiJIUzI1NiJ9.YWNjZXNzLTAxMjM0NTY3ODlhYmNkZWY"
	codexRefreshTok  = "rt-0f8c1a4e6b9d2357c8e1f0a4b7d9e2c5"
	refreshedTok     = "rt-refreshed-9a1c7e2b40d68f35c1a9e7b204d6f8c3"
	brokeredAPIKey   = "sk-proj-8vQ2mNbRt6YwL0xKp4Zc9Hj7"
	testBrokerToken  = "cbt1.pM4nQ7xR2vL9tKc0bY6wZa3fUj8sHd1e"

	// The materialized codex credential as the store holds it, and as the CLI
	// leaves it after a refresh nothing could read back.
	codexStoredAuth = `{"tokens":{"id_token":"","access_token":"` + codexAccessToken +
		`","refresh_token":"` + codexRefreshTok + `","account_id":"acct-redaction"},"auth_mode":"chatgpt"}`
)

// Case 1 (spec §6): the precision the whole per-kind field list exists for. A
// patch that legitimately mentions the literal `chatgpt` — the auth_mode value
// every Codex auth.json carries — and a timestamp must persist untouched. The
// field list plus the length floor is the entire mechanism.
func TestSensitiveSet_ALegitimatePatchMentioningChatgptIsNotASecret(t *testing.T) {
	set := newSensitiveSet([]byte(claudeOAuthToken), []byte(codexStoredAuth))
	sens, err := codexSensitive([]byte(codexStoredAuth))
	if err != nil {
		t.Fatal(err)
	}
	set.add(sens...)

	patch := []byte(`diff --git a/docs/auth.md b/docs/auth.md
+The CLI records auth_mode: "chatgpt" beside last_refresh: 1754380800.
+Both are benign, and neither is a credential.
`)
	if set.scan(patch) {
		t.Errorf("a patch mentioning chatgpt and a timestamp read as a secret: %s", patch)
	}
	if got := set.redact(patch); string(got) != string(patch) {
		t.Errorf("redact rewrote a legitimate patch:\n%s", got)
	}
}

// Case 2: the floor. auth.json carries short benign values, and an exact match
// on one of them would blank or fail every patch that happened to contain it.
func TestSensitiveSet_ValuesBelowTheFloorAreNeverAdded(t *testing.T) {
	const short = "chatgpt1" // 8 bytes: benign, and never a credential
	if len(short) >= minSecretLen {
		t.Fatalf("fixture %q is %d bytes, want one below the %d-byte floor", short, len(short), minSecretLen)
	}
	set := newSensitiveSet([]byte(short))

	body := []byte("the auth_mode is chatgpt1 today")
	if set.scan(body) {
		t.Error("an 8-byte value entered the set; the floor did not hold")
	}
	if got := set.redact(body); string(got) != string(body) {
		t.Errorf("redact = %q, want the body untouched", got)
	}
	// One byte past the floor is a real secret and must be caught.
	set.add([]byte(strings.Repeat("z", minSecretLen)))
	if !set.scan([]byte("prefix" + strings.Repeat("z", minSecretLen) + "suffix")) {
		t.Error("a value exactly at the floor was dropped")
	}
}

// Case 3 (spec §3): a token the CLI refreshed mid-run is unknown to caesar
// until read-back, so the streams are quarantined and only published once the
// set is complete. The publish is atomic and idempotent — a worker that died
// mid-publish retries over the same directory.
func TestPublishLogs_RedactsAMidRunRefreshedTokenAndIsIdempotent(t *testing.T) {
	r := newLeaseRunner(t, newLeaseEngine(), &fakeLeaser{}, nil, Options{DataRoot: t.TempDir()})
	ref := testRef()
	quarantined := "starting up\ntoken refreshed to " + refreshedTok + "\ndone\n"
	seedQuarantine(t, r, ref, map[string]string{
		agentLogFile:   quarantined,
		gatewayLogFile: `{"msg":"egress.summary","requests":1}` + "\n",
	})

	// The set as it stood while the agent ran knows nothing of the new token;
	// the conclusion's read-back is what completes it.
	set := newSensitiveSet([]byte(claudeOAuthToken))
	if set.scan([]byte(quarantined)) {
		t.Fatal("the fixture's refreshed token was already in the set; the case proves nothing")
	}
	set.add([]byte(refreshedTok))

	if err := r.publishLogs(ref, set); err != nil {
		t.Fatalf("publishLogs: %v", err)
	}

	published := readPublished(t, r, ref, agentLogFile)
	if strings.Contains(published, refreshedTok) {
		t.Errorf("the published log still carries the refreshed token:\n%s", published)
	}
	if !strings.Contains(published, redactedMarker) {
		t.Errorf("the published log carries no %s marker:\n%s", redactedMarker, published)
	}
	if !strings.Contains(published, "starting up") {
		t.Errorf("redaction ate the log around the secret:\n%s", published)
	}
	if _, err := os.Stat(r.quarantineDir(ref)); !os.IsNotExist(err) {
		t.Errorf("the quarantine survived publication, stat err = %v", err)
	}
	entries, err := os.ReadDir(r.logsDir(ref))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("published %d files, want both streams (and no temp file left): %v", len(entries), entries)
	}

	// The retry of a worker that died mid-publish finds nothing to do and
	// leaves what is already published exactly as it is.
	if err := r.publishLogs(ref, set); err != nil {
		t.Fatalf("second publishLogs: %v", err)
	}
	if again := readPublished(t, r, ref, agentLogFile); again != published {
		t.Errorf("a second publish rewrote the log:\n%s\nwant:\n%s", again, published)
	}
}

// Case 4 (spec §3, the regression the spec calls out): the patch is stored
// base64-encoded, so a scan over the serialized payload would never find a raw
// token. The scan must run on the canonical decoded bytes — exactly where
// collect.go encodes them.
func TestSensitiveSet_ScansTheCanonicalPatchBytesNotTheEncodedPayload(t *testing.T) {
	set := newSensitiveSet([]byte(claudeOAuthToken))
	rep := gitops.ExtractReport{
		Changed:  true,
		TreeHash: "tree-1",
		Diff:     []byte("diff --git a/leak.txt b/leak.txt\n+" + claudeOAuthToken + "\n"),
	}

	if !set.scan(rep.Diff) {
		t.Error("the canonical patch bytes carry the token, yet the scan missed them")
	}
	encoded := base64.StdEncoding.EncodeToString(rep.Diff)
	if set.scan([]byte(encoded)) {
		t.Error("the encoded payload matched: this test can no longer prove the scan must run pre-encoding")
	}
}

// Case 5 (spec §2.7, §3): the read-back was unusable, so the refreshed value is
// unknowable and NO complete scan exists. Nothing workspace-derived may leave
// the host: no patch, no declared artifacts, no log_ref, no published log — and
// the failure snapshot shrinks to container metadata, because the workspace
// listing is agent-chosen bytes and a filename can carry the very token nobody
// can redact.
func TestExecute_AnUnscannableReadBackWithholdsEverythingWorkspaceDerived(t *testing.T) {
	credID, ref := uuid.New(), testRef()
	dataRoot := t.TempDir()
	eng := newLeaseEngine()
	fakePipeline(t, eng, dataRoot, ref, gitops.ExtractReport{
		Changed: true, BaseSHA: "abc", TreeHash: "tree-1",
		Diff: []byte("diff --git a/NEW.md b/NEW.md\n+work\n"),
	})
	leaser := &fakeLeaser{lease: Lease{Epoch: 1, SecretVersion: 9}, secret: []byte(codexStoredAuth)}
	r := newLeaseRunner(t, eng, leaser, &recordingOpener{}, Options{DataRoot: dataRoot})

	// The CLI refreshed its token and died inside the rewrite (the §2.7
	// window): the new value exists only in the workspace the agent chose to
	// put it in, and in a file caesar can no longer parse.
	eng.onCreate[agentName(ref)] = func(dockerapi.ContainerConfig) error {
		ws := filepath.Join(dataRoot, "attempts", ref.String(), gitops.WorkspaceDir)
		if err := os.WriteFile(filepath.Join(ws, refreshedTok+".txt"), []byte("stolen"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(r.authDir(ref), "auth.json"), []byte(tornCodexJSON), 0o600)
	}

	res, err := r.Execute(t.Context(), leaseRequest(credID, kindAgentAuthFile))

	out := outcomeOf(t, err)
	if out.Class != runs.OutcomePermanent || out.Code != runs.CodeInvalidOutput {
		t.Fatalf("outcome = %s/%s, want permanent/%s — a success whose patch can never publish is not a success",
			out.Class, out.Code, runs.CodeInvalidOutput)
	}
	byType := artifactTypes(res.Artifacts)
	for _, kind := range []string{runs.ArtifactKindPatch, runs.ArtifactKindLogRef, runs.ArtifactKindPlan} {
		if _, ok := byType[kind]; ok {
			t.Errorf("a withheld attempt produced a %s artifact", kind)
		}
	}
	snap, ok := byType[runs.ArtifactKindFailureSnapshot]
	if !ok {
		t.Fatalf("no failure snapshot in %v: an attempt that ran still owes its container metadata", res.Artifacts)
	}
	for _, key := range []string{"log_tail", "workspace"} {
		if _, ok := snap.Payload[key]; ok {
			t.Errorf("the withheld snapshot carries %q, which is agent-derived: %v", key, snap.Payload)
		}
	}
	if _, ok := snap.Payload["exit_code"]; !ok {
		t.Errorf("the withheld snapshot lost its container metadata: %v", snap.Payload)
	}
	// The whole result, rendered exactly as it would be persisted.
	if raw, mErr := json.Marshal(res); mErr != nil {
		t.Fatal(mErr)
	} else if strings.Contains(string(raw), refreshedTok) {
		t.Errorf("the refreshed token rode a persisted artifact out:\n%s", raw)
	}

	if entries, rErr := os.ReadDir(r.logsDir(ref)); rErr == nil && len(entries) != 0 {
		t.Errorf("logs dir holds %v, want nothing published from an unscannable attempt", entries)
	} else if rErr != nil && !os.IsNotExist(rErr) {
		t.Fatal(rErr)
	}
	if _, sErr := os.Stat(filepath.Join(r.quarantineDir(ref), agentLogFile)); sErr != nil {
		t.Errorf("the quarantined log was not retained as evidence: %v", sErr)
	}
}

// The withhold rule stops at what the agent could influence: an attempt whose
// exit says WHY it failed is still typed off its own markers, read from the
// quarantine the publication never emptied. Anything else would report a cap
// window as a generic harness failure and charge it to the node.
func TestCollectResult_WithholdingNeverUntypesTheExit(t *testing.T) {
	r := newLeaseRunner(t, newLeaseEngine(), &fakeLeaser{}, nil, Options{DataRoot: t.TempDir()})
	ref := testRef()
	seedQuarantine(t, r, ref, map[string]string{
		agentLogFile: "Claude AI usage limit reached\n",
	})

	_, err := r.collectResult(t.Context(), collection{
		Ref: ref, Kind: "claude-code", ExitCode: 1,
		Sensitive: newSensitiveSet(), Unscannable: true,
	})

	out := outcomeOf(t, err)
	if out.Class != runs.OutcomeTransient || out.Code != runs.CodeRateLimited {
		t.Errorf("outcome = %s/%s, want transient/%s classified off the quarantined tail",
			out.Class, out.Code, runs.CodeRateLimited)
	}
}

// Case 6 (spec §3, §2.3): the dead worker's in-memory set died with it, so
// adoption rebuilds it from state it can trust — or publishes nothing at all.
// One case per custody combination, plus the two ways the rebuild fails.
func TestAdoptionSensitive_RebuildsTheSetFromTrustedStateOrWithholds(t *testing.T) {
	brokeredManifest := attemptManifest{
		CredentialID: uuid.NewString(), Kind: "model_key", Custody: custodyBrokered,
		HarnessKind: "codex", SecretVersion: 4, BrokerToken: testBrokerToken,
	}
	claudeManifest := attemptManifest{
		CredentialID: uuid.NewString(), Kind: "subscription_token", Custody: custodyMaterialized,
		HarnessKind: "claude-code", SecretVersion: 4,
	}
	codexManifest := attemptManifest{
		CredentialID: uuid.NewString(), Kind: kindAgentAuthFile, Custody: custodyMaterialized,
		HarnessKind: "codex", SecretVersion: 4, Identity: "acct-redaction",
	}

	cases := []struct {
		name        string
		rec         credentialReconciliation
		quarantined string
		wantPublish bool
		wantGone    []string // values the published log must not carry
	}{
		{
			// (a) The static Claude token: one value, materialized straight into
			// the agent's environment, recoverable from the store alone.
			name: "claude subscription_token rebuilds from the stored bytes",
			rec: credentialReconciliation{
				Bound: true, HasManifest: true, Manifest: claudeManifest,
				Stored: []byte(claudeOAuthToken), StoredOK: true,
			},
			quarantined: "authenticated with " + claudeOAuthToken + "\n",
			wantPublish: true,
			wantGone:    []string{claudeOAuthToken},
		},
		{
			// (b) The Codex auth file: the driver's per-kind field list over the
			// stored bytes, plus whatever the read-back recovered — the refreshed
			// token exists nowhere else.
			name: "codex agent_auth_file rebuilds both the initial and the refreshed tokens",
			rec: credentialReconciliation{
				Bound: true, HasManifest: true, Manifest: codexManifest,
				Stored: []byte(codexStoredAuth), StoredOK: true,
				Concluded: true,
				Conc: syncConclusion{
					Status:             concSynced,
					RefreshedSensitive: [][]byte{[]byte(refreshedTok)},
				},
			},
			quarantined: "start " + codexAccessToken + " then " + refreshedTok + " end\n",
			wantPublish: true,
			wantGone:    []string{codexAccessToken, codexRefreshTok, refreshedTok},
		},
		{
			// (c) Brokered custody: the raw credential enters the set whole (the
			// driver's field list describes an auth.json, not an API key), and
			// the per-attempt broker token comes off the manifest — it exists
			// nowhere else once its worker is gone.
			name: "brokered model_key rebuilds the broker token and the raw credential",
			rec: credentialReconciliation{
				Bound: true, HasManifest: true, Manifest: brokeredManifest,
				Stored: []byte(brokeredAPIKey), StoredOK: true,
			},
			quarantined: "GET /v1/models with " + testBrokerToken + " for " + brokeredAPIKey + "\n",
			wantPublish: true,
			wantGone:    []string{testBrokerToken, brokeredAPIKey},
		},
		{
			// (d) A crash before the manifest was durable, on an attempt the
			// lease row proves carried a credential: nothing names what to scan
			// for, so nothing publishes.
			name:        "a credential-bearing attempt with no manifest withholds",
			rec:         credentialReconciliation{Bound: true},
			quarantined: "authenticated with " + claudeOAuthToken + "\n",
		},
		{
			// (e) The operator rotated mid-attempt, so the store cannot give the
			// delivered bytes back. Every kind, the static one included: the old
			// value entered the crashed attempt's environment just the same, and
			// only version equality proves the store still holds it.
			name: "a store past the delivered version withholds, static kinds included",
			rec: credentialReconciliation{
				Bound: true, HasManifest: true, Manifest: claudeManifest,
				Stored: []byte("whatever-the-operator-pasted-instead"), StoredOK: false,
			},
			quarantined: "authenticated with " + claudeOAuthToken + "\n",
		},
		{
			// An attempt with no credential at all: the manifest write precedes
			// every container, so a corpse beside no manifest and no lease never
			// held a secret. The empty set IS the complete set.
			name:        "an attempt that never carried a credential publishes as before",
			rec:         credentialReconciliation{},
			quarantined: "nothing secret here\n",
			wantPublish: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newLeaseRunner(t, newLeaseEngine(), &fakeLeaser{}, nil, Options{DataRoot: t.TempDir()})
			ref := testRef()
			seedQuarantine(t, r, ref, map[string]string{agentLogFile: tc.quarantined})

			set, ok := adoptionSensitive(tc.rec)
			if ok != tc.wantPublish {
				t.Fatalf("rebuild ok = %v, want %v", ok, tc.wantPublish)
			}
			if !ok {
				if set != nil {
					t.Errorf("a refused rebuild handed back a set: %v", set)
				}
				return
			}
			if err := r.publishLogs(ref, set); err != nil {
				t.Fatalf("publishLogs: %v", err)
			}
			published := readPublished(t, r, ref, agentLogFile)
			for _, secret := range tc.wantGone {
				if strings.Contains(published, secret) {
					t.Errorf("the published log still carries %q:\n%s", secret, published)
				}
			}
			if len(tc.wantGone) > 0 && !strings.Contains(published, redactedMarker) {
				t.Errorf("nothing was redacted from:\n%s", published)
			}
		})
	}
}

// The broker token is the one secret the manifest carries, and adoption's whole
// ability to redact it rests on that persistence surviving a round trip through
// the file. Nothing else in the system reads it back.
func TestExecute_ABrokeredAttemptPersistsItsBrokerTokenForAdoption(t *testing.T) {
	credID, ref := uuid.New(), testRef()
	dataRoot := t.TempDir()
	eng := newLeaseEngine()
	eng.onCreate[stageName(ref)] = func(dockerapi.ContainerConfig) error {
		return writeContract(filepath.Join(dataRoot, "attempts", ref.String(), gitops.StageReportFile),
			gitops.StageReport{Verified: true, HeadSHA: "abc"})
	}
	var agentEnv []string
	eng.onCreate[agentName(ref)] = func(cfg dockerapi.ContainerConfig) error {
		agentEnv = append([]string(nil), cfg.Env...)
		return errors.New("stop here: the agent's environment is already visible")
	}
	r := newLeaseRunner(t, eng, &fakeLeaser{}, &recordingOpener{}, Options{DataRoot: dataRoot})

	if _, err := r.Execute(t.Context(), leaseRequest(credID, "model_key")); err == nil {
		t.Fatal("want the attempt to die at the agent, past the manifest write")
	}

	handed := envToMap(agentEnv)["OPENAI_API_KEY"]
	if handed == "" {
		t.Fatalf("the brokered agent got no token: %v", agentEnv)
	}
	m, err := r.readManifest(ref)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if m.BrokerToken != handed {
		t.Errorf("manifest broker token = %q, want the %q the agent was handed", m.BrokerToken, handed)
	}
	// The proof it is redaction material and not decoration: the rebuilt set
	// blanks the token out of a log that carries it.
	set, ok := adoptionSensitive(credentialReconciliation{
		Bound: true, HasManifest: true, Manifest: m,
		Stored: []byte(brokeredAPIKey), StoredOK: true,
	})
	if !ok {
		t.Fatal("the rebuild refused a manifest this attempt wrote itself")
	}
	if !set.scan([]byte("Proxy-Authorization: " + handed)) {
		t.Error("the rebuilt set does not know the broker token the agent held")
	}
}

// The reconciled twin of the live path's priority order: what the READ-BACK
// found outranks what the agent exited with. A quarantined credential is a
// permanent, operator-visible fact; reporting the exit's transient
// harness_error instead would schedule a retry doomed to meet the same broken
// credential.
func TestAdopt_AQuarantinedCredentialOutranksTheExitDerivedOutcome(t *testing.T) {
	f := newReconFixture(t, "exited", 1)
	f.seedCrashedAttempt(t, "harness fell over\n")
	rewriteAuthFile(t, f.r, f.ref, poisonedCodexJSON)
	// The store still holds exactly the version this attempt was delivered:
	// the precondition adoption's rebuild rests on, and the shared fixture's
	// lease-granted version is not the opener's.
	f.r.creds = fixedOpener{secret: []byte(codexAuthJSON), version: openedSecretVersion}
	m, err := f.r.readManifest(f.ref)
	if err != nil {
		t.Fatal(err)
	}
	m.SecretVersion = openedSecretVersion
	if err := f.r.writeManifest(f.ref, m); err != nil {
		t.Fatal(err)
	}

	res, err := f.r.Inspect(t.Context(), f.ref)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !res.Completed || res.Outcome == nil {
		t.Fatalf("result = %+v, want the quarantine adopted as the attempt's outcome", res)
	}
	if res.Outcome.Class != runs.OutcomePermanent || res.Outcome.Code != runs.CodeCredentialQuarantined {
		t.Errorf("outcome = %s/%s, want permanent/%s — never a transient the scheduler will retry",
			res.Outcome.Class, res.Outcome.Code, runs.CodeCredentialQuarantined)
	}
	if q := f.leaser.quarantineCalls(); len(q) != 1 {
		t.Errorf("RecordQuarantine calls = %+v, want exactly one", q)
	}
	// Task 7 withheld adoption's failure snapshot because it could not rebuild
	// the redaction set. It can now, so the adopted failure owes the same
	// evidence the worker path attaches to its own.
	byType := artifactTypes(res.Result.Artifacts)
	if _, ok := byType[runs.ArtifactKindFailureSnapshot]; !ok {
		t.Errorf("the adopted failure carries no snapshot: %v", res.Result.Artifacts)
	}
	if _, ok := byType[runs.ArtifactKindLogRef]; !ok {
		t.Errorf("the adopted failure carries no log_ref: %v", res.Result.Artifacts)
	}
}

// Every byte the agent could have written into is scanned before it is
// accepted, and a hit fails the whole attempt closed: the offending artifact
// is not persisted, and neither is anything beside it. Both routes out of the
// workspace are covered — the extracted patch and the harness's own declared
// documents.
func TestExecute_ASecretInAWorkspaceArtifactFailsClosed(t *testing.T) {
	const cleanDiff = "diff --git a/NOTES.md b/NOTES.md\n+nothing sensitive here\n"
	cases := []struct {
		name    string
		diff    string
		declare string // .caesar/out/artifacts.json contents; "" = declare nothing
	}{
		{
			name: "the extracted patch",
			// The token is planted in the CANONICAL bytes; collect encodes them
			// only after the scan has run.
			diff: "diff --git a/leak.txt b/leak.txt\n+" + codexAccessToken + "\n",
		},
		{
			name: "a declared artifact",
			diff: cleanDiff,
			declare: fmt.Sprintf(`[{"type":"plan","payload":{"content":"I logged in with %s"}}]`,
				codexAccessToken),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			credID, ref := uuid.New(), testRef()
			dataRoot := t.TempDir()
			eng := newLeaseEngine()
			fakePipeline(t, eng, dataRoot, ref, gitops.ExtractReport{
				Changed: true, BaseSHA: "abc", TreeHash: "tree-1", Diff: []byte(tc.diff),
			})
			leaser := &fakeLeaser{lease: Lease{Epoch: 1, SecretVersion: 9}, secret: []byte(codexStoredAuth)}
			r := newLeaseRunner(t, eng, leaser, &recordingOpener{}, Options{DataRoot: dataRoot})
			eng.onCreate[agentName(ref)] = func(dockerapi.ContainerConfig) error {
				if tc.declare == "" {
					return nil
				}
				out := filepath.Join(dataRoot, "attempts", ref.String(), gitops.WorkspaceDir, ".caesar", "out")
				if err := os.MkdirAll(out, 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(out, "artifacts.json"), []byte(tc.declare), 0o600)
			}

			res, err := r.Execute(t.Context(), leaseRequest(credID, kindAgentAuthFile))

			out := outcomeOf(t, err)
			if out.Class != runs.OutcomePolicyViolation || out.Code != runs.CodeSecretInArtifact {
				t.Fatalf("outcome = %s/%s, want policy_violation/%s", out.Class, out.Code, runs.CodeSecretInArtifact)
			}
			for _, kind := range []string{runs.ArtifactKindPatch, runs.ArtifactKindPlan} {
				if _, ok := artifactTypes(res.Artifacts)[kind]; ok {
					t.Errorf("a %s artifact was persisted anyway: %v", kind, res.Artifacts)
				}
			}
			if raw, mErr := json.Marshal(res); mErr != nil {
				t.Fatal(mErr)
			} else if strings.Contains(string(raw), codexAccessToken) {
				t.Errorf("the credential rode a persisted artifact out:\n%s", raw)
			}
		})
	}
}

// fixedOpener is the credential store answering with exactly what an attempt
// was delivered — bytes and version together. That pairing is what proves the
// store can still give the crashed attempt's own credential back, which is
// adoption's licence to publish anything at all.
type fixedOpener struct {
	secret  []byte
	version int64
}

func (o fixedOpener) OpenCredential(context.Context, uuid.UUID, string) ([]byte, int64, error) {
	return o.secret, o.version, nil
}

// seedQuarantine writes the streams a running attempt would have left in
// quarantine, before anything has been published.
func seedQuarantine(t *testing.T, r *Runner, ref runs.AttemptRef, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(r.quarantineDir(ref), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(r.quarantineDir(ref), name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func readPublished(t *testing.T, r *Runner, ref runs.AttemptRef, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.logsDir(ref), name))
	if err != nil {
		t.Fatalf("read published %s: %v", name, err)
	}
	return string(raw)
}

func artifactTypes(arts []runs.NewArtifact) map[string]runs.NewArtifact {
	out := make(map[string]runs.NewArtifact, len(arts))
	for _, a := range arts {
		out[a.Type] = a
	}
	return out
}

// fakePipeline scripts the trusted halves of the pipeline against the fake
// daemon: a staged workspace the agent can write into, and an extraction that
// reports the given patch. The agent hook is the caller's to set.
func fakePipeline(t *testing.T, eng *leaseEngine, dataRoot string, ref runs.AttemptRef, rep gitops.ExtractReport) {
	t.Helper()
	dir := filepath.Join(dataRoot, "attempts", ref.String())
	eng.onCreate[stageName(ref)] = func(dockerapi.ContainerConfig) error {
		if err := os.MkdirAll(filepath.Join(dir, gitops.WorkspaceDir), 0o700); err != nil {
			return err
		}
		return writeContract(filepath.Join(dir, gitops.StageReportFile),
			gitops.StageReport{Verified: true, HeadSHA: "abc"})
	}
	eng.onCreate[extractName(ref)] = func(dockerapi.ContainerConfig) error {
		return writeContract(filepath.Join(dir, gitops.ExtractReportFile), rep)
	}
}
