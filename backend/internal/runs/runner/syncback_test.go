package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// The read-back fixtures. Every well-formed one carries the refresh token the
// commit shape requires; identity is what separates a CLI refresh (same
// account) from an agent-authored substitution (any other account).
const (
	refreshedCodexJSON = `{"tokens":{"access_token":"at-2","refresh_token":"rt-2","account_id":"acct-1"}}`
	poisonedCodexJSON  = `{"tokens":{"access_token":"at-x","refresh_token":"rt-x","account_id":"acct-evil"}}`
	tornCodexJSON      = `{"tokens": {"acc`
	noRefreshCodexJSON = `{"tokens":{"access_token":"at-2","account_id":"acct-1"}}`
	noIdentityJSON     = `{"tokens":{"refresh_token":"rt-9"}}`
)

// seedSyncFixture puts one leased attempt's credential material on disk the
// way Execute does — granted bytes in the auth dir, manifest beside it — and
// returns the manifest a conclusion would run against: epoch 1, version 9,
// identity pinned to the granted account.
func seedSyncFixture(t *testing.T, r *Runner, ref runs.AttemptRef, credID uuid.UUID) attemptManifest {
	t.Helper()
	if err := r.materializeAuthDir(ref, codexDriver(t), []byte(codexAuthJSON)); err != nil {
		t.Fatalf("materializeAuthDir: %v", err)
	}
	identity, err := codexIdentity([]byte(codexAuthJSON))
	if err != nil {
		t.Fatal(err)
	}
	m := attemptManifest{
		CredentialID: credID.String(), Kind: kindAgentAuthFile, Custody: custodyMaterialized,
		HarnessKind: "codex", SecretVersion: 9, Epoch: 1,
		AuthDir: r.relPath(r.authDir(ref)), Identity: identity,
		secretBytes: []byte(codexAuthJSON),
	}
	if err := r.writeManifest(ref, m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	return m
}

// rewriteAuthFile is the agent's mutation: whatever the container left in
// auth.json when it stopped.
func rewriteAuthFile(t *testing.T, r *Runner, ref runs.AttemptRef, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.authDir(ref), "auth.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertAuthEvidence pins what the conclusion left on disk: retained means
// both the auth dir and the manifest are still there (evidence for the Task 10
// orphan sweep or a reconciler); cleaned means both are gone.
func assertAuthEvidence(t *testing.T, r *Runner, ref runs.AttemptRef, retained bool) {
	t.Helper()
	_, dirErr := os.Stat(r.authDir(ref))
	_, manErr := os.Stat(r.manifestPath(ref))
	if retained {
		if dirErr != nil {
			t.Errorf("auth dir gone (%v), want it retained as evidence", dirErr)
		}
		if manErr != nil {
			t.Errorf("manifest gone (%v), want it retained as evidence", manErr)
		}
		return
	}
	if !os.IsNotExist(dirErr) {
		t.Errorf("auth dir stat err = %v, want it removed", dirErr)
	}
	if !os.IsNotExist(manErr) {
		t.Errorf("manifest stat err = %v, want it removed", manErr)
	}
}

func hasSensitive(conc syncConclusion, v string) bool {
	for _, s := range conc.RefreshedSensitive {
		if string(s) == v {
			return true
		}
	}
	return false
}

// The §6 crash matrix, file half: every way an attempt's credential
// obligation can end, and what each one must leave behind — in the store, on
// the disk, and in the conclusion the caller acts on.
func TestConcludeCredential_CrashMatrix(t *testing.T) {
	cases := []struct {
		name            string
		prepare         func(t *testing.T, r *Runner, ref runs.AttemptRef, l *fakeLeaser, credID uuid.UUID) attemptManifest
		script          func(l *fakeLeaser)
		deadCtx         bool
		wantStatus      string
		wantCommits     int
		wantReleases    int
		wantQuarantines int
		wantOutcome     bool
		check           func(t *testing.T, r *Runner, ref runs.AttemptRef, l *fakeLeaser, conc syncConclusion)
	}{
		{
			// Row 1: an env-delivered credential mounts nothing, so there is
			// nothing to read back and nothing to guard — release and done.
			name: "no auth mount concludes clean with a bare release",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				return attemptManifest{
					CredentialID: credID.String(), Kind: "subscription_token",
					Custody: custodyMaterialized, SecretVersion: 9, Epoch: 1,
				}
			},
			wantStatus: "clean", wantReleases: 1,
		},
		{
			// Row 2: a crash before the CLI ever wrote leaves no file; nothing
			// can be scanned, the store stays as it was, and the evidence
			// stays for the orphan sweep to reap once the lease is gone.
			name: "missing file is unscannable and keeps the evidence",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				if err := os.Remove(filepath.Join(r.authDir(ref), "auth.json")); err != nil {
					t.Fatal(err)
				}
				return m
			},
			wantStatus: "unscannable", wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
			},
		},
		{
			// Row 3: the §2.7 torn-write window — the CLI died mid-rewrite.
			name: "torn write is unscannable and keeps the evidence",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, tornCodexJSON)
				return m
			},
			wantStatus: "unscannable", wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
			},
		},
		{
			// Row 4a: a bounded read of a regular file is the only read there
			// is — anything past the limit is not a credential.
			name: "oversize file is unscannable and keeps the evidence",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, strings.Repeat("a", authFileReadLimit+1))
				return m
			},
			wantStatus: "unscannable", wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
			},
		},
		{
			// Row 4b: a fifo would block the first read forever, so the gate
			// has to refuse it from the Lstat alone — a hang here IS the
			// failure this row guards against.
			name: "non-regular file is refused without being opened",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				path := filepath.Join(r.authDir(ref), "auth.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				return m
			},
			wantStatus: "unscannable", wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
			},
		},
		{
			// Row 5: the CLI never wrote — nothing to commit, and the file
			// leaves the host under the same fence a refresh would.
			name: "unchanged bytes conclude clean and clean up",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				return seedSyncFixture(t, r, ref, credID)
			},
			wantStatus: "clean", wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, false)
			},
		},
		{
			// Row 6: the CLI refreshed its own tokens — the one case where the
			// store learns something, gated on the manifest's epoch + version.
			name: "refreshed same-identity bytes are committed",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, refreshedCodexJSON)
				return m
			},
			wantStatus: "synced", wantCommits: 1, wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, l *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, false)
				if got := l.commitCalls()[0].secret; !bytes.Equal(got, []byte(refreshedCodexJSON)) {
					t.Errorf("committed %q, want the refreshed file's exact bytes", got)
				}
			},
		},
		{
			// Row 6, retry half: a commit-then-crash retry answers
			// already_applied — same conclusion, no double anything.
			name: "already applied commit is the same conclusion",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, refreshedCodexJSON)
				return m
			},
			script:     func(l *fakeLeaser) { l.commitStatus = SyncStatusAlreadyApplied },
			wantStatus: "synced", wantCommits: 1, wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, false)
			},
		},
		{
			// Row 7: an operator rotated mid-attempt; the stale snapshot dies
			// quietly (the discard is audited assets-side).
			name: "cas lost discards the stale snapshot quietly",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, refreshedCodexJSON)
				return m
			},
			script:     func(l *fakeLeaser) { l.commitStatus = SyncStatusCASLost },
			wantStatus: "discarded", wantCommits: 1, wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, false)
			},
		},
		{
			// Row 8: a fenced commit means a reconciler owns the lease and the
			// directory both — this worker releases nothing and deletes
			// nothing.
			name: "fenced commit relinquishes lease and evidence",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, refreshedCodexJSON)
				return m
			},
			script:     func(l *fakeLeaser) { l.commitStatus = SyncStatusFenced },
			wantStatus: "fenced", wantCommits: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
			},
		},
		{
			// Row 9: agent-authored substitution (spec §2.5) — a well-formed
			// file naming somebody else's account. The store is preserved, the
			// outcome is typed, and the poisoned tokens still count as
			// sensitive: they may be live secrets of some account.
			name: "identity mismatch quarantines and preserves the store",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, poisonedCodexJSON)
				return m
			},
			wantStatus: "quarantined", wantReleases: 1, wantQuarantines: 1, wantOutcome: true,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, conc syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
				if conc.Outcome.Class != runs.OutcomePermanent || conc.Outcome.Code != runs.CodeCredentialQuarantined {
					t.Errorf("outcome = %s/%s, want permanent/%s", conc.Outcome.Class, conc.Outcome.Code, runs.CodeCredentialQuarantined)
				}
				if !hasSensitive(conc, "rt-x") || !hasSensitive(conc, "at-x") {
					t.Errorf("RefreshedSensitive = %q, want the poisoned file's tokens in it", conc.RefreshedSensitive)
				}
			},
		},
		{
			// Row 10, commit half (spec §6): a paused worker resumes after a
			// takeover advanced the epoch. Its commit is refused, and it may
			// delete nothing and release nothing.
			name: "epoch fence race on a refreshed file commits nothing",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, refreshedCodexJSON)
				return m
			},
			script:     func(l *fakeLeaser) { l.currentEpoch = 2 },
			wantStatus: "fenced", wantCommits: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
			},
		},
		{
			// Row 10, cleanup half: the unchanged shortcut still proves
			// ownership before removing — a fenced heartbeat means the
			// directory is the reconciler's recovery evidence now.
			name: "epoch fence race on an unchanged file deletes nothing",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				return seedSyncFixture(t, r, ref, credID)
			},
			script:     func(l *fakeLeaser) { l.currentEpoch = 2 },
			wantStatus: "fenced",
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
			},
		},
		{
			// Row 11: the wall-deadline exit — the most important caller is
			// the one whose context is already dead, and the bounded
			// WithoutCancel derivation is what keeps its lease from stranding.
			name: "dead parent context still commits and releases",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, refreshedCodexJSON)
				return m
			},
			deadCtx:    true,
			wantStatus: "synced", wantCommits: 1, wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, false)
			},
		},
		{
			// Row 12: the lease-livelock trap. A commit that is doomed to a
			// permanent validation error must never be attempted — the janitor
			// would retry the same invalid file forever, holding the lease as
			// an eternal credential_busy. The release is what frees the next
			// claimant.
			name: "well-formed file without a refresh token is refused before commit",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, l *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, noRefreshCodexJSON)
				if _, _, err := l.Acquire(context.Background(), credID, ref.String()); err != nil {
					t.Fatalf("seeding the held lease: %v", err)
				}
				return m
			},
			wantStatus: "unscannable", wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, l *fakeLeaser, _ syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
				if _, _, err := l.Acquire(context.Background(), uuid.New(), "run-x-implement-2"); err != nil {
					t.Errorf("acquire after the conclusion failed (%v): the lease was never freed", err)
				}
			},
		},
		{
			// Row 12, defense in depth: assets' validator may drift ahead of
			// the runner's read gate, and its ClassInvalid answer is just as
			// permanent — release, keep the evidence, never retry.
			name: "commit rejected as invalid still releases the lease",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, refreshedCodexJSON)
				return m
			},
			script:     func(l *fakeLeaser) { l.commitErr = xerrors.E(xerrors.ClassInvalid, "refresh_token is required") },
			wantStatus: "unscannable", wantCommits: 1, wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, conc syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
				if !hasSensitive(conc, "rt-2") {
					t.Errorf("RefreshedSensitive = %q, want the read-back tokens in it", conc.RefreshedSensitive)
				}
			},
		},
		{
			// A file the identity extractor cannot read is not completely
			// scanned, whatever else parsed: same terminal shape as any other
			// unscannable read-back, tokens still redactable.
			name: "identity-free file is unscannable after the read gate",
			prepare: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, credID uuid.UUID) attemptManifest {
				m := seedSyncFixture(t, r, ref, credID)
				rewriteAuthFile(t, r, ref, noIdentityJSON)
				return m
			},
			wantStatus: "unscannable", wantReleases: 1,
			check: func(t *testing.T, r *Runner, ref runs.AttemptRef, _ *fakeLeaser, conc syncConclusion) {
				assertAuthEvidence(t, r, ref, true)
				if !hasSensitive(conc, "rt-9") {
					t.Errorf("RefreshedSensitive = %q, want the read-back tokens in it", conc.RefreshedSensitive)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			credID, ref := uuid.New(), testRef()
			leaser := &fakeLeaser{}
			if tc.script != nil {
				tc.script(leaser)
			}
			r := newLeaseRunner(t, newLeaseEngine(), leaser, nil, Options{DataRoot: t.TempDir()})
			m := tc.prepare(t, r, ref, leaser, credID)
			ctx := t.Context()
			if tc.deadCtx {
				dead, cancel := context.WithCancel(context.Background())
				cancel()
				ctx = dead
			}

			conc := r.concludeCredential(ctx, ref, m, credID)

			if conc.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", conc.Status, tc.wantStatus)
			}
			commits := leaser.commitCalls()
			if len(commits) != tc.wantCommits {
				t.Errorf("CommitSync calls = %d, want %d: %+v", len(commits), tc.wantCommits, commits)
			}
			for i, c := range commits {
				if c.credentialID != credID || c.ref != ref.String() || c.epoch != 1 || c.expectVersion != 9 {
					t.Errorf("commit[%d] = %+v, want the manifest's credential at epoch 1, version 9", i, c)
				}
			}
			_, _, released := leaser.calls()
			if len(released) != tc.wantReleases {
				t.Errorf("Release calls = %d, want %d: %+v", len(released), tc.wantReleases, released)
			}
			for i, rel := range released {
				if rel.credentialID != credID || rel.ref != ref.String() || rel.epoch != 1 {
					t.Errorf("release[%d] = %+v, want the attempt's own credential at epoch 1", i, rel)
				}
			}
			if q := leaser.quarantineCalls(); len(q) != tc.wantQuarantines {
				t.Errorf("RecordQuarantine calls = %d, want %d: %+v", len(q), tc.wantQuarantines, q)
			}
			if (conc.Outcome != nil) != tc.wantOutcome {
				t.Errorf("conclusion outcome = %+v, want present=%v", conc.Outcome, tc.wantOutcome)
			}
			if tc.wantOutcome && conc.Outcome != nil && !strings.Contains(conc.Outcome.Message, credID.String()) {
				t.Errorf("outcome message %q does not name credential %s", conc.Outcome.Message, credID)
			}
			if tc.check != nil {
				tc.check(t, r, ref, leaser, conc)
			}
		})
	}
}

// A runner wired without the credential aggregate cannot prove it still owns
// anything: the fenced remover refuses — files intact — rather than panicking
// on a nil leaser.
func TestRemoveAuthDirFenced_NilLeaserRefuses(t *testing.T) {
	ref := testRef()
	r := newLeaseRunner(t, newLeaseEngine(), nil, nil, Options{DataRoot: t.TempDir()})
	seedAuthDir(t, r, ref)

	if r.removeAuthDirFenced(t.Context(), ref, uuid.New(), 1) {
		t.Error("a leaser-less runner claimed it removed the auth directory")
	}
	if _, err := os.Stat(filepath.Join(r.authDir(ref), "auth.json")); err != nil {
		t.Errorf("the refusal still destroyed the auth file: %v", err)
	}
	if _, err := os.Stat(r.manifestPath(ref)); err != nil {
		t.Errorf("the refusal still destroyed the manifest: %v", err)
	}
}
