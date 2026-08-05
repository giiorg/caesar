package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/egress"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// leaseCall is one recorded call against the leaser: enough to prove which
// credential was named, on whose behalf, and under which fence.
type leaseCall struct {
	credentialID uuid.UUID
	ref          string
	epoch        int64
}

// fakeLeaser is the assets aggregate reduced to what the runner can observe:
// a scripted acquire and a call log. Everything else answers success, so a
// test only has to script the one thing it is about.
type fakeLeaser struct {
	CredentialLeaser // embed nil: an unexpected call panics loudly

	mu           sync.Mutex
	acquireErr   error
	lease        Lease
	secret       []byte
	heartbeatErr func(n int) error

	acquired    []leaseCall
	heartbeats  []leaseCall
	released    []leaseCall
	authExpired []leaseCall
}

func (l *fakeLeaser) Acquire(_ context.Context, id uuid.UUID, ref string) (Lease, []byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquired = append(l.acquired, leaseCall{credentialID: id, ref: ref})
	if l.acquireErr != nil {
		return Lease{}, nil, l.acquireErr
	}
	return l.lease, l.secret, nil
}

func (l *fakeLeaser) Heartbeat(_ context.Context, id uuid.UUID, ref string, epoch int64) error {
	l.mu.Lock()
	n := len(l.heartbeats) + 1
	l.heartbeats = append(l.heartbeats, leaseCall{credentialID: id, ref: ref, epoch: epoch})
	hb := l.heartbeatErr
	l.mu.Unlock()
	if hb != nil {
		return hb(n)
	}
	return nil
}

func (l *fakeLeaser) Release(_ context.Context, id uuid.UUID, ref string, epoch int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, leaseCall{credentialID: id, ref: ref, epoch: epoch})
	return nil
}

func (l *fakeLeaser) MarkAuthExpired(_ context.Context, id uuid.UUID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.authExpired = append(l.authExpired, leaseCall{credentialID: id})
	return nil
}

// markedAuthExpired is the freshness signal's call log: which credentials this
// runner told the aggregate their provider had rejected.
func (l *fakeLeaser) markedAuthExpired() []leaseCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]leaseCall(nil), l.authExpired...)
}

func (l *fakeLeaser) calls() (acquired, heartbeats, released []leaseCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]leaseCall(nil), l.acquired...),
		append([]leaseCall(nil), l.heartbeats...),
		append([]leaseCall(nil), l.released...)
}

// leaseEngine is a daemon that records what an attempt asked of it. Nothing
// here starts real work: the point is which calls happened and in what order
// relative to the credential gate, so every method is scripted and the whole
// pipeline can be stopped at a chosen container.
type leaseEngine struct {
	fakeEngine

	mu       sync.Mutex
	created  []string                                         // container names, in order
	onCreate map[string]func(dockerapi.ContainerConfig) error // per name: observe, and optionally refuse
}

func newLeaseEngine() *leaseEngine {
	return &leaseEngine{
		fakeEngine: fakeEngine{inspect: func(string) (dockerapi.ImageInspect, error) {
			return dockerapi.ImageInspect{RepoDigests: []string{"ghcr.io/x/codex@sha256:beef"}}, nil
		}},
		onCreate: map[string]func(dockerapi.ContainerConfig) error{},
	}
}

func (e *leaseEngine) NetworkInspect(context.Context, string) (dockerapi.NetworkInspect, error) {
	return dockerapi.NetworkInspect{}, nil // the shared egress network is already up
}

func (e *leaseEngine) NetworkCreate(_ context.Context, name string, _ bool, _ map[string]string) (string, error) {
	e.record("network:" + name)
	return name, nil
}

func (e *leaseEngine) NetworkRemove(context.Context, string) error { return nil }

func (e *leaseEngine) NetworkConnect(context.Context, string, string, []string) error { return nil }

// A scripted container produces no output; the streams exist so an attempt
// can run past the gateway to the agent.
func (e *leaseEngine) ContainerLogs(context.Context, string, bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (e *leaseEngine) ContainerKill(context.Context, string, string) error { return nil }

func (e *leaseEngine) ContainerInspect(context.Context, string) (dockerapi.ContainerInspect, error) {
	return dockerapi.ContainerInspect{}, nil
}

func (e *leaseEngine) ContainerCreate(_ context.Context, name string, cfg dockerapi.ContainerConfig,
	_ dockerapi.HostConfig, _ map[string]dockerapi.EndpointSettings,
) (string, error) {
	e.record(name)
	e.mu.Lock()
	hook := e.onCreate[name]
	e.mu.Unlock()
	if hook != nil {
		if err := hook(cfg); err != nil {
			return "", err
		}
	}
	return "id-" + name, nil
}

func (e *leaseEngine) ContainerStart(context.Context, string) error         { return nil }
func (e *leaseEngine) ContainerWait(context.Context, string) (int64, error) { return 0, nil }
func (e *leaseEngine) ContainerRemove(context.Context, string, bool) error  { return nil }

func (e *leaseEngine) record(what string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.created = append(e.created, what)
}

func (e *leaseEngine) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.created...)
}

// newLeaseRunner builds a runner over the scripted daemon and leaser, with a
// codex harness — the kind whose credential is an agent_auth_file.
func newLeaseRunner(t *testing.T, eng Engine, leaser CredentialLeaser, opener CredentialOpener, opts Options) *Runner {
	t.Helper()
	if opts.DataRoot == "" {
		opts.DataRoot = t.TempDir()
	}
	opts.CaesarImage, opts.GitImage = "caesar:dev", "caesar-git:dev"
	r, err := New(eng, &harnessRegistry{spec: HarnessSpec{Kind: "codex", ImageRef: "ghcr.io/x/codex@sha256:beef"}},
		opener, leaser, stubGit{}, nil, opts, discardLog())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func leaseRequest(credentialID uuid.UUID, kind string) runs.ExecutionRequest {
	return runs.ExecutionRequest{
		Ref:        testRef(),
		Kind:       runs.KindImplement,
		HarnessID:  uuid.New(),
		Credential: runs.CredentialBinding{ID: credentialID, Kind: kind},
		Repo:       runs.RepoRef{Forge: "github", Slug: "giiorg/demo"},
		BaseSHA:    "abc",
	}
}

func outcomeOf(t *testing.T, err error) runs.Outcome {
	t.Helper()
	var oe runs.OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("error %v is not a typed outcome", err)
	}
	return oe.Outcome
}

// A credential another attempt is already holding is somebody else's turn,
// not this attempt's failure: the runner reports the zero-charge wait before
// it has created a single container, so nothing usage-bearing ever ran.
func TestExecute_LeaseHeldElsewhereWaitsWithoutRunningAnything(t *testing.T) {
	credID := uuid.New()
	leaser := &fakeLeaser{acquireErr: xerrors.Wrap(xerrors.ClassConflict, "held by another attempt", ErrLeaseBusy)}
	opener := &recordingOpener{}
	eng := newLeaseEngine()
	r := newLeaseRunner(t, eng, leaser, opener, Options{})

	_, err := r.Execute(t.Context(), leaseRequest(credID, kindAgentAuthFile))

	out := outcomeOf(t, err)
	if out.Class != runs.OutcomeTransient || out.Code != runs.CodeCredentialBusy {
		t.Errorf("outcome = %s/%s, want transient/credential_busy", out.Class, out.Code)
	}
	if !strings.Contains(out.Message, credID.String()) {
		t.Errorf("message %q does not name the contended credential %s", out.Message, credID)
	}

	acquired, _, _ := leaser.calls()
	if len(acquired) != 1 || acquired[0].credentialID != credID || acquired[0].ref != testRef().String() {
		t.Fatalf("acquire calls = %+v, want exactly the bound credential for this attempt", acquired)
	}
	if seen := eng.seen(); len(seen) != 0 {
		t.Errorf("the daemon was asked for %v, want nothing — a wait must cost no usage-bearing work", seen)
	}
	if len(opener.opened) != 0 {
		t.Errorf("opened %v, want nothing: a leased credential is never opened separately", opener.opened)
	}
}

// Static kinds are shareable: nothing a running attempt does can change a
// model key, so serializing on one would only queue work behind itself. The
// manifest is still written — adoption needs the credential identity to
// rebuild the redaction set whatever the custody.
func TestExecute_StaticCredentialTakesNoLeaseButStillGetsAManifest(t *testing.T) {
	credID := uuid.New()
	leaser := &fakeLeaser{}
	eng := newLeaseEngine()
	// Stop the attempt one step past the manifest write.
	eng.onCreate[stageName(testRef())] = func(dockerapi.ContainerConfig) error {
		return errors.New("daemon out of room")
	}
	dataRoot := t.TempDir()
	r := newLeaseRunner(t, eng, leaser, &recordingOpener{}, Options{DataRoot: dataRoot})

	if _, err := r.Execute(t.Context(), leaseRequest(credID, "model_key")); err == nil {
		t.Fatal("want the attempt to die at the stage container, past the manifest write")
	}

	if acquired, _, _ := leaser.calls(); len(acquired) != 0 {
		t.Errorf("acquire calls = %+v, want none: a static kind takes no lease", acquired)
	}
	m, err := r.readManifest(testRef())
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if m.CredentialID != credID.String() || m.Kind != "model_key" || m.Custody != custodyBrokered {
		t.Errorf("manifest = %+v, want the bound id, kind model_key, brokered custody", m)
	}
	if m.Epoch != 0 || m.SecretVersion != 0 {
		t.Errorf("manifest carries lease fields %d/%d for an unleased kind, want 0/0", m.Epoch, m.SecretVersion)
	}
}

// The manifest is durable before any container exists, and the bytes the
// gateway is handed are the very snapshot the acquire transaction returned:
// a leased credential is never opened a second time, because a rotation
// between the two reads would make sync-back's CAS discard a real refresh.
func TestExecute_AcquiredLeaseMaterializesTheGrantedSnapshot(t *testing.T) {
	credID := uuid.New()
	const granted = "{\"tokens\":{\"refresh_token\":\"granted-by-the-lease\"}}"
	leaser := &fakeLeaser{lease: Lease{Epoch: 1, SecretVersion: 7}, secret: []byte(granted)}
	opener := &recordingOpener{}
	eng := newLeaseEngine()
	dataRoot := t.TempDir()
	ref := testRef()

	// The stage container must find a manifest already on disk, and it
	// reports success so the attempt reaches the gateway.
	var manifestAtStage attemptManifest
	var manifestErr error
	eng.onCreate[stageName(ref)] = func(dockerapi.ContainerConfig) error {
		manifestAtStage, manifestErr = readContract[attemptManifest](
			filepath.Join(dataRoot, "manifests", ref.String()+".json"), manifestLimit)
		return writeContract(filepath.Join(dataRoot, "attempts", ref.String(), gitops.StageReportFile),
			gitops.StageReport{Verified: true, HeadSHA: "abc"})
	}
	// The gateway is the one container the provider credential reaches;
	// capturing its config is how the delivered bytes become observable.
	var gatewayEnv []string
	eng.onCreate[gatewayName(ref)] = func(cfg dockerapi.ContainerConfig) error {
		gatewayEnv = append([]string(nil), cfg.Env...)
		return errors.New("stop here: the delivered credential is already visible")
	}

	r := newLeaseRunner(t, eng, leaser, opener, Options{DataRoot: dataRoot})
	if _, err := r.Execute(t.Context(), leaseRequest(credID, kindAgentAuthFile)); err == nil {
		t.Fatal("want the attempt to die at the gateway, past staging")
	}

	if manifestErr != nil {
		t.Fatalf("no readable manifest when the stage container was created: %v", manifestErr)
	}
	if manifestAtStage.Epoch != 1 || manifestAtStage.SecretVersion != 7 {
		t.Errorf("manifest at stage time = epoch %d, version %d; want the granted 1/7",
			manifestAtStage.Epoch, manifestAtStage.SecretVersion)
	}
	if manifestAtStage.Custody != custodyMaterialized {
		t.Errorf("custody = %q, want %q", manifestAtStage.Custody, custodyMaterialized)
	}
	if manifestAtStage.BrokerToken != "" {
		t.Error("a materialized attempt's manifest carries a broker token, want none")
	}

	if len(opener.opened) != 0 {
		t.Errorf("opened %v, want nothing: the acquire snapshot is the only source", opener.opened)
	}
	// Materialized custody: the gateway is the allowlisted route out and
	// nothing more. The granted snapshot goes to the CLI's own auth
	// directory, so a gateway holding it would be a second copy of a secret
	// that is supposed to exist in one place.
	cfg := gatewayConfigFromEnv(t, gatewayEnv)
	if cfg.Provider.Credential != "" || cfg.Provider.Endpoint != "" {
		t.Errorf("gateway provider = %+v, want none under materialized custody", cfg.Provider)
	}
	if cfg.TokenSHA256 != "" {
		t.Error("a gateway that brokers nothing has nobody to authenticate, want no token digest")
	}
	if strings.Contains(strings.Join(gatewayEnv, " "), granted) {
		t.Error("the granted snapshot reached the gateway's environment, want it nowhere near")
	}

	_, _, released := leaser.calls()
	if len(released) != 1 || released[0].credentialID != credID || released[0].epoch != 1 {
		t.Errorf("release calls = %+v, want exactly one at the granted epoch", released)
	}
}

// A materialized credential is delivered by the driver into the agent's own
// world, and the gateway never sees it. Claude Code's is the env-delivered
// case: the opened token is what the CLI authenticates with, and the broker
// half of the environment is absent entirely.
func TestExecute_MaterializedCredentialIsDeliveredToTheAgentNotTheGateway(t *testing.T) {
	eng := newLeaseEngine()
	ref := testRef()
	dataRoot := t.TempDir()
	eng.onCreate[stageName(ref)] = func(dockerapi.ContainerConfig) error {
		return writeContract(filepath.Join(dataRoot, "attempts", ref.String(), gitops.StageReportFile),
			gitops.StageReport{Verified: true, HeadSHA: "abc"})
	}
	var gatewayEnv, agentEnv []string
	eng.onCreate[gatewayName(ref)] = func(cfg dockerapi.ContainerConfig) error {
		gatewayEnv = append([]string(nil), cfg.Env...)
		return nil
	}
	eng.onCreate[agentName(ref)] = func(cfg dockerapi.ContainerConfig) error {
		agentEnv = append([]string(nil), cfg.Env...)
		return errors.New("stop here: the delivered environment is already visible")
	}

	r, err := New(eng, &harnessRegistry{spec: HarnessSpec{Kind: "claude-code", ImageRef: "ghcr.io/x/claude@sha256:beef"}},
		&recordingOpener{}, &fakeLeaser{}, stubGit{}, nil,
		Options{DataRoot: dataRoot, CaesarImage: "caesar:dev", GitImage: "caesar-git:dev"}, discardLog())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Execute(t.Context(), leaseRequest(uuid.New(), "subscription_token")); err == nil {
		t.Fatal("want the attempt to die at the agent, past the gateway")
	}

	env := envToMap(agentEnv)
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "provider-key" {
		t.Errorf("agent env token = %q, want the opened credential", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	for _, k := range []string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER"} {
		if env[k] != "1" {
			t.Errorf("agent env %s = %q, want the kill-switch on", k, env[k])
		}
	}
	for _, k := range []string{"CAESAR_BROKER_URL", "CAESAR_BROKER_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY"} {
		if _, ok := env[k]; ok {
			t.Errorf("agent env carries %s, but a materialized attempt brokers nothing", k)
		}
	}
	if strings.Contains(strings.Join(gatewayEnv, " "), "provider-key") {
		t.Error("the credential reached the gateway, want it only in the agent's own environment")
	}
}

// gatewayConfigFromEnv decodes the config document the gateway container is
// handed — the one place an attempt's egress posture is stated.
func gatewayConfigFromEnv(t *testing.T, env []string) egress.Config {
	t.Helper()
	for _, e := range env {
		_, raw, ok := strings.Cut(e, "=")
		if !ok || !strings.HasPrefix(e, "CAESAR_EGRESS_CONFIG=") {
			continue
		}
		var cfg egress.Config
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("decode egress config: %v", err)
		}
		return cfg
	}
	t.Fatalf("no egress config in the gateway environment %v", env)
	return egress.Config{}
}

// The lease has to be kept alive for as long as the attempt runs, and a
// heartbeat the store refuses means a takeover happened: custody now belongs
// to the reconciler, so the attempt is cut short rather than left running
// against a credential it no longer owns.
func TestLeaseHeartbeat_TicksUntilStoppedAndCancelsOnAFence(t *testing.T) {
	credID, ref := uuid.New(), testRef()

	t.Run("ticks at the configured interval and stops cleanly", func(t *testing.T) {
		leaser := &fakeLeaser{}
		r := newLeaseRunner(t, newLeaseEngine(), leaser, nil,
			Options{testLeaseHeartbeatInterval: 5 * time.Millisecond})

		stop := r.startLeaseHeartbeat(t.Context(), credID, ref, 3, func() {
			t.Error("a healthy heartbeat must not cancel the attempt")
		})
		waitFor(t, func() bool { _, hb, _ := leaser.calls(); return len(hb) >= 3 })
		stop()

		_, before, _ := leaser.calls()
		time.Sleep(30 * time.Millisecond)
		_, after, _ := leaser.calls()
		if len(after) != len(before) {
			t.Errorf("%d heartbeats arrived after stop, want none — the goroutine outlived its attempt", len(after)-len(before))
		}
		for _, hb := range before {
			if hb.credentialID != credID || hb.ref != ref.String() || hb.epoch != 3 {
				t.Fatalf("heartbeat %+v, want the attempt's own credential at epoch 3", hb)
			}
		}
	})

	t.Run("a fenced heartbeat cancels the attempt", func(t *testing.T) {
		leaser := &fakeLeaser{heartbeatErr: func(int) error {
			return xerrors.E(xerrors.ClassConflict, "credential lease fenced")
		}}
		r := newLeaseRunner(t, newLeaseEngine(), leaser, nil,
			Options{testLeaseHeartbeatInterval: 5 * time.Millisecond})

		fenced := make(chan struct{})
		var once sync.Once
		stop := r.startLeaseHeartbeat(t.Context(), credID, ref, 1, func() { once.Do(func() { close(fenced) }) })
		defer stop()

		select {
		case <-fenced:
		case <-time.After(5 * time.Second):
			t.Fatal("a fenced heartbeat never cancelled the attempt")
		}
	})

	t.Run("a transient store error is not a fence", func(t *testing.T) {
		leaser := &fakeLeaser{heartbeatErr: func(int) error { return errors.New("connection reset") }}
		r := newLeaseRunner(t, newLeaseEngine(), leaser, nil,
			Options{testLeaseHeartbeatInterval: 5 * time.Millisecond})

		stop := r.startLeaseHeartbeat(t.Context(), credID, ref, 1, func() {
			t.Error("a database blip cancelled a healthy attempt")
		})
		defer stop()
		waitFor(t, func() bool { _, hb, _ := leaser.calls(); return len(hb) >= 3 })
	})
}

// The pinned interval is what production runs at; a test that shortened it
// would prove nothing about the shipped posture if the constant drifted.
func TestLeaseHeartbeatInterval_IsThePinnedThirtySeconds(t *testing.T) {
	if leaseHeartbeatInterval != 30*time.Second {
		t.Errorf("leaseHeartbeatInterval = %s, want 30s", leaseHeartbeatInterval)
	}
	r := newLeaseRunner(t, newLeaseEngine(), &fakeLeaser{}, nil, Options{})
	if got := r.heartbeatInterval(); got != leaseHeartbeatInterval {
		t.Errorf("unconfigured runner heartbeats every %s, want the pinned %s", got, leaseHeartbeatInterval)
	}
}

// The manifest is a file other processes read after a crash, so it is never
// visible half-written and never world-readable.
func TestWriteManifest_IsAtomicAndOwnerOnly(t *testing.T) {
	r := newLeaseRunner(t, newLeaseEngine(), &fakeLeaser{}, nil, Options{})
	ref := testRef()
	if err := r.writeManifest(ref, attemptManifest{CredentialID: "a", Kind: kindAgentAuthFile, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.writeManifest(ref, attemptManifest{CredentialID: "b", Kind: kindAgentAuthFile, Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(r.manifestPath(ref))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("manifest mode = %o, want 600", perm)
	}
	m, err := r.readManifest(ref)
	if err != nil {
		t.Fatal(err)
	}
	if m.CredentialID != "b" || m.Epoch != 2 {
		t.Errorf("manifest = %+v, want the second write to have replaced the first whole", m)
	}
	entries, err := os.ReadDir(r.manifestsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("manifests dir holds %d files, want 1 — a temp file survived", len(entries))
	}
}

// waitFor polls a condition rather than sleeping a guessed duration.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
