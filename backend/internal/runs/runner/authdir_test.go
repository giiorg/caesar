package runner

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// codexAuthJSON is a well-formed auth.json: the refresh token assets validates
// on import, and enough account identity for the driver to pin one.
const codexAuthJSON = `{"tokens":{"access_token":"at-1","refresh_token":"rt-1","account_id":"acct-1"}}`

func codexDriver(t *testing.T) driver {
	t.Helper()
	d, err := driverFor("codex")
	if err != nil {
		t.Fatalf("driverFor(codex): %v", err)
	}
	return d
}

// The materialized credential is a secret at rest on the host: only the
// control plane's own uid may read it, and the directory it sits in is
// attempt-private. It also lives outside the attempt directory, because
// teardown removes that whole tree on every return path and the refreshed
// file has to outlive the containers (spec §2.4).
func TestMaterializeAuthDir_Permissions(t *testing.T) {
	r := newLeaseRunner(t, newLeaseEngine(), &fakeLeaser{}, nil, Options{})
	ref := testRef()
	d := codexDriver(t)
	secret := []byte(codexAuthJSON)

	if err := r.materializeAuthDir(ref, d, secret); err != nil {
		t.Fatalf("materializeAuthDir: %v", err)
	}

	dir, err := os.Stat(r.authDir(ref))
	if err != nil {
		t.Fatalf("stat auth dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("auth dir mode = %o, want 700", perm)
	}
	path := filepath.Join(r.authDir(ref), d.authMount.file)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", d.authMount.file, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 600", d.authMount.file, perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, secret) {
		t.Errorf("materialized %d bytes, want the granted secret's %d", len(raw), len(secret))
	}
	if strings.HasPrefix(r.authDir(ref), r.attemptDir(ref)) {
		t.Errorf("auth dir %s sits inside the attempt dir %s, which teardown removes whole",
			r.authDir(ref), r.attemptDir(ref))
	}
}

// The cleanup fence (spec §2.4, §6): a worker paused past a takeover resumes
// into its own deferred cleanup, and by then the directory is the reconciler's
// recovery evidence. The database epoch fence cannot stop a local
// os.RemoveAll, so the removal proves ownership first — a refused heartbeat
// means relinquish, not delete.
func TestRemoveAuthDirFenced_FencedWorkerRelinquishes(t *testing.T) {
	credID, ref := uuid.New(), testRef()
	leaser := &fakeLeaser{heartbeatErr: func(int) error {
		return xerrors.E(xerrors.ClassConflict, "credential lease fenced")
	}}
	r := newLeaseRunner(t, newLeaseEngine(), leaser, nil, Options{})
	seedAuthDir(t, r, ref)

	if r.removeAuthDirFenced(t.Context(), ref, credID, 1) {
		t.Error("a fenced worker reported it had removed the auth directory")
	}

	if _, err := os.Stat(filepath.Join(r.authDir(ref), "auth.json")); err != nil {
		t.Errorf("the fenced worker destroyed the recovery file: %v", err)
	}
	if _, err := os.Stat(r.manifestPath(ref)); err != nil {
		t.Errorf("the fenced worker destroyed the manifest: %v", err)
	}
	_, hb, _ := leaser.calls()
	if len(hb) != 1 || hb[0].credentialID != credID || hb[0].ref != ref.String() || hb[0].epoch != 1 {
		t.Errorf("heartbeats = %+v, want exactly one for this attempt at its own epoch", hb)
	}
}

// The owning worker's own cleanup: the obligation is over, so the credential
// leaves the host with the manifest that described it.
func TestRemoveAuthDirFenced_OwnerRemovesTheDirectoryAndTheManifest(t *testing.T) {
	credID, ref := uuid.New(), testRef()
	leaser := &fakeLeaser{}
	r := newLeaseRunner(t, newLeaseEngine(), leaser, nil, Options{})
	seedAuthDir(t, r, ref)

	if !r.removeAuthDirFenced(t.Context(), ref, credID, 4) {
		t.Fatal("the lease owner was refused its own cleanup")
	}

	if _, err := os.Stat(r.authDir(ref)); !os.IsNotExist(err) {
		t.Errorf("auth dir survived its owner's cleanup, stat err = %v", err)
	}
	if _, err := os.Stat(r.manifestPath(ref)); !os.IsNotExist(err) {
		t.Errorf("manifest survived its owner's cleanup, stat err = %v", err)
	}
	// Removing what is already gone is the reconciliation path's normal case,
	// and it must stay quiet rather than report a failed conclusion.
	if !r.removeAuthDirFenced(t.Context(), ref, credID, 4) {
		t.Error("a second removal reported a fence, want the same success")
	}
}

// The materialized file path end to end, short of a real daemon: the granted
// snapshot is on disk before the agent container is created, the bind hands
// the directory to the CLI at the driver's own path in the daemon's spelling
// of it, and the manifest records everything recovery needs — the version the
// CAS will be compared against, the epoch that fences it, and the account
// identity read-back is pinned to (spec §2.3, §2.5).
func TestExecute_CodexAuthFileIsMaterializedBoundAndPinnedInTheManifest(t *testing.T) {
	credID, ref := uuid.New(), testRef()
	dataRoot := t.TempDir()
	eng := newLeaseEngine()
	eng.onCreate[stageName(ref)] = func(dockerapi.ContainerConfig) error {
		return writeContract(filepath.Join(dataRoot, "attempts", ref.String(), gitops.StageReportFile),
			gitops.StageReport{Verified: true, HeadSHA: "abc"})
	}
	// The agent is the moment of truth: whatever the CLI is going to read has
	// to be on the host already, because the bind is resolved at create time.
	var agentEnv []string
	var atAgent []byte
	var readErr error
	eng.onCreate[agentName(ref)] = func(cfg dockerapi.ContainerConfig) error {
		agentEnv = append([]string(nil), cfg.Env...)
		atAgent, readErr = os.ReadFile(filepath.Join(dataRoot, "auth", ref.String(), "auth.json"))
		return errors.New("stop here: the agent's world is already visible")
	}
	leaser := &fakeLeaser{lease: Lease{Epoch: 2, SecretVersion: 9}, secret: []byte(codexAuthJSON)}

	r := newLeaseRunner(t, eng, leaser, &recordingOpener{},
		Options{DataRoot: dataRoot, DataHostRoot: "/srv/caesar-data"})
	if _, err := r.Execute(t.Context(), leaseRequest(credID, kindAgentAuthFile)); err == nil {
		t.Fatal("want the attempt to die at the agent, past materialization")
	}

	if readErr != nil {
		t.Fatalf("the agent was created before its credential existed: %v", readErr)
	}
	if !bytes.Equal(atAgent, []byte(codexAuthJSON)) {
		t.Errorf("materialized %q, want the bytes the lease granted", atAgent)
	}
	want := "/srv/caesar-data/auth/" + ref.String() + ":/caesar-auth"
	binds := eng.hostFor(t, agentName(ref)).Binds
	if !slices.Contains(binds, want) {
		t.Errorf("agent binds = %v, want the auth directory at %q in the daemon's spelling", binds, want)
	}
	if got := envToMap(agentEnv)["CODEX_HOME"]; got != "/caesar-auth" {
		t.Errorf("CODEX_HOME = %q, want the mount the bind lands on", got)
	}

	m, err := r.readManifest(ref)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	identity, err := codexIdentity([]byte(codexAuthJSON))
	if err != nil {
		t.Fatal(err)
	}
	if m.AuthDir != filepath.Join("auth", ref.String()) {
		t.Errorf("manifest auth dir = %q, want the path relative to the data root", m.AuthDir)
	}
	if m.Identity != identity || m.Identity == "" {
		t.Errorf("manifest identity = %q, want the granted file's own %q", m.Identity, identity)
	}
	if m.SecretVersion != 9 || m.Epoch != 2 {
		t.Errorf("manifest version/epoch = %d/%d, want the granted 9/2", m.SecretVersion, m.Epoch)
	}
	raw, err := os.ReadFile(r.manifestPath(ref))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "rt-1") {
		t.Error("the manifest serialized the credential itself, want only the version and identity")
	}

	// Teardown removed the attempt tree; the credential the CLI may have
	// refreshed is the one thing that has to outlive it (spec §2.4).
	if _, err := os.Stat(r.attemptDir(ref)); !os.IsNotExist(err) {
		t.Errorf("attempt dir survived teardown, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.authDir(ref), "auth.json")); err != nil {
		t.Errorf("teardown took the auth file with it: %v", err)
	}
}

// The auth machinery is gated on custody, not on the harness: the same Codex
// driver holding a model key is brokered, and a raw API key is not an
// auth.json. Materializing one would put a secret on disk for a CLI that will
// never look for it, and hand the identity extractor bytes it cannot read.
func TestExecute_BrokeredCodexMaterializesNoAuthDirectory(t *testing.T) {
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
		return errors.New("stop here: the agent's world is already visible")
	}
	leaser := &fakeLeaser{}

	r := newLeaseRunner(t, eng, leaser, &recordingOpener{}, Options{DataRoot: dataRoot})
	if _, err := r.Execute(t.Context(), leaseRequest(credID, "model_key")); err == nil {
		t.Fatal("want the attempt to die at the agent")
	}

	if _, err := os.Stat(r.authDir(ref)); !os.IsNotExist(err) {
		t.Errorf("a brokered attempt materialized an auth directory, stat err = %v", err)
	}
	if binds := eng.hostFor(t, agentName(ref)).Binds; len(binds) != 1 {
		t.Errorf("agent binds = %v, want the workspace alone", binds)
	}
	if _, ok := envToMap(agentEnv)["CODEX_HOME"]; ok {
		t.Error("a brokered agent was pointed at an auth directory that does not exist")
	}
	if acquired, _, _ := leaser.calls(); len(acquired) != 0 {
		t.Errorf("acquire calls = %+v, want none: a model key is shareable", acquired)
	}

	m, err := r.readManifest(ref)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if m.AuthDir != "" || m.Identity != "" {
		t.Errorf("manifest = %+v, want no auth dir and no identity pin under brokered custody", m)
	}
	// Every kind records the version it was delivered at: crash adoption may
	// only publish an attempt's evidence while the store still holds it.
	if m.SecretVersion != openedSecretVersion {
		t.Errorf("manifest version = %d, want the %d the open reported", m.SecretVersion, openedSecretVersion)
	}
	if m.Epoch != 0 {
		t.Errorf("manifest epoch = %d, want 0 for a kind that takes no lease", m.Epoch)
	}
}

// A credential caesar cannot pin an identity to can never have a refresh
// committed back (spec §2.5 compares read-back against this very value), so
// the attempt is refused where it is still free — before a container exists —
// and the lease it took is handed straight back.
func TestExecute_CredentialWithNoAccountIdentityIsRefusedBeforeAnyContainer(t *testing.T) {
	credID, ref := uuid.New(), testRef()
	eng := newLeaseEngine()
	leaser := &fakeLeaser{
		lease:  Lease{Epoch: 1, SecretVersion: 1},
		secret: []byte(`{"tokens":{"refresh_token":"rt-1"}}`), // shape-valid, nobody's account
	}
	r := newLeaseRunner(t, eng, leaser, &recordingOpener{}, Options{DataRoot: t.TempDir()})

	_, err := r.Execute(t.Context(), leaseRequest(credID, kindAgentAuthFile))

	out := outcomeOf(t, err)
	if out.Class != runs.OutcomePermanent || out.Code != runs.CodeAuthExpired {
		t.Errorf("outcome = %s/%s, want permanent/%s", out.Class, out.Code, runs.CodeAuthExpired)
	}
	if !strings.Contains(out.Message, credID.String()) {
		t.Errorf("message %q does not name the credential the operator has to fix", out.Message)
	}
	if seen := eng.seen(); len(seen) != 0 {
		t.Errorf("the daemon was asked for %v, want nothing", seen)
	}
	if _, err := os.Stat(r.authDir(ref)); !os.IsNotExist(err) {
		t.Errorf("an unusable credential was still written to disk, stat err = %v", err)
	}
	if _, _, released := leaser.calls(); len(released) != 1 {
		t.Errorf("release calls = %+v, want exactly one: a refusal must not strand the lease", released)
	}
}

// seedAuthDir puts one attempt's credential material on disk: the auth
// directory the CLI would read and the manifest that describes it.
func seedAuthDir(t *testing.T, r *Runner, ref runs.AttemptRef) {
	t.Helper()
	if err := r.materializeAuthDir(ref, codexDriver(t), []byte(codexAuthJSON)); err != nil {
		t.Fatalf("materializeAuthDir: %v", err)
	}
	if err := r.writeManifest(ref, attemptManifest{CredentialID: uuid.New().String(), Kind: kindAgentAuthFile}); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
}
