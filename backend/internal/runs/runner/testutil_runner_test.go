//go:build runnerintegration

package runner

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// The runner suite drives a real Docker daemon. It needs the two images the
// pipeline runs (`task image:build`) and a reachable socket. Missing images
// are a setup gap and skip; a missing daemon fails loudly, because silently
// skipping the only tests that prove the isolation story is worse than red.
const (
	dockerSocket = "/var/run/docker.sock"
	caesarImage  = "caesar:dev"
	gitImage     = "caesar-git:dev"
)

type sinkEvent struct {
	Ref     runs.AttemptRef
	Kind    string
	Payload map[string]any
}

type recordingSink struct {
	mu  sync.Mutex
	evs []sinkEvent
}

func (s *recordingSink) AppendRuntimeEvent(_ context.Context, ref runs.AttemptRef, kind string, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evs = append(s.evs, sinkEvent{Ref: ref, Kind: kind, Payload: payload})
	return nil
}

func (s *recordingSink) events() []sinkEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinkEvent(nil), s.evs...)
}

type staticResolver struct{ spec HarnessSpec }

func (r staticResolver) HarnessSpec(context.Context, uuid.UUID) (HarnessSpec, error) {
	return r.spec, nil
}

type staticCreds struct{ value []byte }

func (c staticCreds) OpenCredential(context.Context, uuid.UUID, string) ([]byte, error) {
	return c.value, nil
}

// fixtureGit stands in for the forge: the remote is a bare repo on this
// machine, bind-mounted read-only into the stage container.
type fixtureGit struct{ dir string }

func (g fixtureGit) CloneSpec(context.Context, runs.RepoRef) (CloneSpec, error) {
	return CloneSpec{URL: "file://" + remoteMount + "/bare.git", BindDir: g.dir}, nil
}

// The fixture repository is built once per test binary: InitBareRepo pins the
// author, the committer and both dates, so its head SHA is a constant.
var (
	fixtureOnce sync.Once
	fixtureDir  string
	fixtureHead string
	fixtureErr  error
)

func fixtureRepo(t *testing.T) (dir, sha string) {
	t.Helper()
	fixtureOnce.Do(func() {
		fixtureDir, fixtureErr = os.MkdirTemp("", "caesar-runner-fixture-")
		if fixtureErr != nil {
			return
		}
		fixtureHead, fixtureErr = fake.InitBareRepo(fixtureDir)
	})
	if fixtureErr != nil {
		t.Fatalf("fixture repo: %v", fixtureErr)
	}
	return fixtureDir, fixtureHead
}

func fixtureSHA(t *testing.T) string {
	t.Helper()
	_, sha := fixtureRepo(t)
	return sha
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtureDir != "" {
		_ = os.RemoveAll(fixtureDir)
	}
	os.Exit(code)
}

// scriptedHarness runs a shell script as the agent. caesar-git:dev carries
// busybox sh, git and wget — everything the probes need — so a scripted
// agent exercises the real container path without a real harness image.
func scriptedHarness(endpoint, script string) HarnessSpec {
	return HarnessSpec{
		Kind:             "claude-code",
		ImageRef:         gitImage,
		ProviderEndpoint: endpoint,
		Entrypoint:       []string{"/bin/sh", "-c"},
		Cmd:              []string{script},
	}
}

// credentialLog records what the fake provider was actually presented with.
type credentialLog struct {
	mu       sync.Mutex
	apiKeys  []string
	authzs   []string
	paths    []string
	requests int
}

func (c *credentialLog) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	c.apiKeys = append(c.apiKeys, r.Header.Get("X-Api-Key"))
	c.authzs = append(c.authzs, r.Header.Get("Authorization"))
	c.paths = append(c.paths, r.URL.Path)
}

func (c *credentialLog) snapshot() credentialLog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return credentialLog{apiKeys: c.apiKeys, authzs: c.authzs, paths: c.paths, requests: c.requests}
}

// startFakeProvider runs the "provider" in this test process and hands back
// the URL a container reaches it on. That URL is the one test-only exception
// in the whole pipeline: Options.testHostAccess maps host.docker.internal for
// the gateway container, and for nothing else.
func startFakeProvider(t *testing.T) (endpoint string, seen *credentialLog) {
	t.Helper()
	seen = &credentialLog{}
	// The gateway container reaches this server through host.docker.internal,
	// which on Linux is the docker0 bridge address rather than loopback — so
	// the listener must cover every interface, not httptest's 127.0.0.1.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{
		Listener: ln,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.record(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-fable-5"}]}`))
		})},
	}
	srv.Start()
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return "http://host.docker.internal:" + u.Port(), seen
}

// newDockerRunner is the suite's constructor: a real daemon client, the
// fixture forge, a static credential, and tight ceilings so a runaway test
// costs seconds rather than the shipped defaults.
func newDockerRunner(t *testing.T, opts Options, hs HarnessSpec, sink EventSink) *Runner {
	t.Helper()
	if _, err := os.Stat(dockerSocket); err != nil {
		t.Fatalf("runner suite needs a Docker daemon at %s: %v", dockerSocket, err)
	}
	engine := dockerapi.New(dockerSocket)
	requireImages(t, engine)

	if opts.DataRoot == "" {
		opts.DataRoot = t.TempDir()
	}
	if opts.EgressNetwork == "" {
		// Unique per runner so concurrent suites never share one network.
		opts.EgressNetwork = "caesar-egress-test-" + uuid.NewString()[:8]
	}
	opts.CaesarImage, opts.GitImage = caesarImage, gitImage
	if opts.WorkspaceQuotaBytes == 0 {
		opts.WorkspaceQuotaBytes = 64 << 20
	}
	if opts.WorkspaceQuotaFiles == 0 {
		opts.WorkspaceQuotaFiles = 2000
	}
	if opts.LogCapBytes == 0 {
		opts.LogCapBytes = 256 << 10
	}
	if opts.LogFloodBytes == 0 {
		opts.LogFloodBytes = 1 << 20
	}
	if opts.QuotaPollInterval == 0 {
		opts.QuotaPollInterval = 200 * time.Millisecond
	}
	opts.testHostAccess = true

	dir, _ := fixtureRepo(t)
	r, err := New(engine, staticResolver{spec: hs}, staticCreds{value: []byte("real-provider-key")}, nil,
		fixtureGit{dir: dir}, sink, opts, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	started := time.Now().Unix()
	t.Cleanup(func() { sweep(t, engine, opts.EgressNetwork, started) })
	return r
}

// requireImages skips rather than fails when the pipeline's images are not
// built: that is a setup gap, and the message says how to close it.
func requireImages(t *testing.T, engine *dockerapi.Client) {
	t.Helper()
	for _, ref := range []string{caesarImage, gitImage} {
		if _, err := engine.ImageInspect(context.Background(), ref); err != nil {
			t.Skipf("image %s is missing (run `task image:build`): %v", ref, err)
		}
	}
}

// dockerRef is a fresh execution identity per test: every container and
// network the pipeline creates is named from it, so a unique ref is what
// keeps two tests — or a leftover from a killed run — out of each other's way.
func dockerRef() runs.AttemptRef {
	return runs.AttemptRef{RunID: uuid.New(), NodeID: "implement", Attempt: 1}
}

// sweep removes what this test left behind: the containers and networks it
// created (by creation time, so nothing predating the test is touched) and
// its own egress network, which teardown does not own.
func sweep(t *testing.T, engine *dockerapi.Client, egressNet string, since int64) {
	t.Helper()
	ctx := context.Background()
	if list, err := engine.ContainerListByLabel(ctx, "caesar.managed=true"); err == nil {
		for _, c := range list {
			if c.Created >= since {
				_ = engine.ContainerRemove(ctx, c.ID, true)
			}
		}
	}
	if list, err := engine.NetworkListByLabel(ctx, "caesar.managed=true"); err == nil {
		for _, n := range list {
			created, err := time.Parse(time.RFC3339Nano, n.Created)
			if err == nil && created.Unix() >= since && n.Name != egressNet {
				_ = engine.NetworkRemove(ctx, n.ID)
			}
		}
	}
	// The gateway's endpoint may still be detaching right after removal.
	for range 10 {
		if err := engine.NetworkRemove(ctx, egressNet); err == nil || dockerapi.IsNotFound(err) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("egress network %s survived the sweep", egressNet)
}

func assertNoContainer(t *testing.T, r *Runner, name string) {
	t.Helper()
	if _, err := r.engine.ContainerInspect(context.Background(), name); !dockerapi.IsNotFound(err) {
		t.Errorf("container %s still exists (inspect err = %v)", name, err)
	}
}

func assertNoNetwork(t *testing.T, r *Runner, name string) {
	t.Helper()
	if _, err := r.engine.NetworkInspect(context.Background(), name); !dockerapi.IsNotFound(err) {
		t.Errorf("network %s still exists (inspect err = %v)", name, err)
	}
}

func readLogFile(t *testing.T, r *Runner, ref runs.AttemptRef, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.logsDir(ref), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// decodedDiff reads the patch payload's diff the way every consumer must:
// the field is base64 by contract, so that a patch carrying non-UTF-8 bytes
// survives the JSON boundary intact.
func decodedDiff(t *testing.T, payload map[string]any) string {
	t.Helper()
	encoded, ok := payload["diff"].(string)
	if !ok {
		t.Fatalf("patch payload has no diff string: %v", payload)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("patch diff is not base64: %v", err)
	}
	return string(raw)
}

func artifactsByType(arts []runs.NewArtifact) map[string]runs.NewArtifact {
	out := make(map[string]runs.NewArtifact, len(arts))
	for _, a := range arts {
		out[a.Type] = a
	}
	return out
}

func implementRequest(t *testing.T, ref runs.AttemptRef, baseSHA string) runs.ExecutionRequest {
	t.Helper()
	return runs.ExecutionRequest{
		Ref:        ref,
		Kind:       runs.KindImplement,
		HarnessID:  uuid.New(),
		Credential: runs.CredentialBinding{ID: uuid.New(), Kind: "model_key"},
		Repo:       runs.RepoRef{Forge: "github", Slug: "giiorg/demo"},
		BaseSHA:    baseSHA,
		Deadline:   time.Now().Add(5 * time.Minute),
	}
}
