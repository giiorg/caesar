//go:build runnerintegration

package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
	"github.com/giiorg/caesar/backend/internal/runs/runner"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

const (
	e2eDockerSocket = "/var/run/docker.sock"
	e2eCaesarImage  = "caesar:dev"
	e2eGitImage     = "caesar-git:dev"
	// remoteMount mirrors runner's package-private const: the stage
	// container mounts the bare repo here, so the clone URL names it.
	e2eRemoteMount = "/remote"
)

// scriptedResolver wraps the PRODUCTION appHarnessResolver and injects the
// agent's Entrypoint/Cmd. In production a HarnessSpec leaves those nil so the
// image's own default command runs (Plan 07's driver contract) — a bare
// `caesar` invocation exits 2 with usage text and never writes a patch. This
// seam is exactly where Plan 07's drivers will script the harness; wrapping
// (not replacing) keeps the production adapter — its Kind/ImageRef/
// CredentialID translation and the audited credential read that the runner
// triggers off CredentialID — under test.
type scriptedResolver struct {
	inner  appHarnessResolver
	script string
}

func (s scriptedResolver) HarnessSpec(ctx context.Context, id uuid.UUID) (runner.HarnessSpec, error) {
	spec, err := s.inner.HarnessSpec(ctx, id)
	if err != nil {
		return runner.HarnessSpec{}, err
	}
	spec.Entrypoint = []string{"/bin/sh", "-c"}
	spec.Cmd = []string{s.script}
	return spec, nil
}

// localBareGit stands in for the forge's clone URL: the remote is a bare repo
// on this host, bind-mounted read-only into the stage container and cloned
// over file://. Production appGitSource builds an HTTPS URL from the repo's
// forge + slug (and self-hosted base URLs are Plan 08's); this local file://
// path is the fake forge the runner suite also uses. The credential.used
// audit row this test asserts is unaffected — it comes from the harness
// credential the runner opens per attempt through the production adapter, not
// from the clone.
type localBareGit struct{ dir string }

func (g localBareGit) CloneSpec(context.Context, runs.RepoRef) (runner.CloneSpec, error) {
	return runner.CloneSpec{URL: "file://" + e2eRemoteMount + "/bare.git", BindDir: g.dir}, nil
}

// TestRunnerE2E_TaskThroughRealContainerToPatch drives the whole spine with
// the REAL runner: create a project/repo/credential/harness through the
// services, publish a one-node workflow, create the task through the runs
// service with a local bare repo as the forge (fake forge for ResolveHead —
// Plan 08 owns the real one), then run the orchestrator until the run is
// terminal, asserting the run reached a patch through a real container.
func TestRunnerE2E_TaskThroughRealContainerToPatch(t *testing.T) {
	if _, err := os.Stat(e2eDockerSocket); err != nil {
		t.Skipf("runner e2e needs a Docker daemon at %s: %v", e2eDockerSocket, err)
	}
	ctx := context.Background()
	engine := dockerapi.New(e2eDockerSocket)
	for _, ref := range []string{e2eCaesarImage, e2eGitImage} {
		if _, err := engine.ImageInspect(ctx, ref); err != nil {
			t.Skipf("image %s is missing (run `task image:build`): %v", ref, err)
		}
	}

	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	clk := clock.System{}
	rec := audit.NewRecorder(clk)
	keys, err := secrets.ParseKeyset("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	identitySvc := identity.NewService(pool, clk, rec, identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	adminUser, err := identitySvc.Bootstrap(ctx, "root@example.com", "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	admin := authn.Principal{UserID: adminUser.ID, Email: adminUser.Email, Role: authn.RoleAdmin}

	assetsSvc := assets.NewService(pool, clk, rec, keys)
	workflowsSvc := workflows.NewService(pool, clk, rec)

	// The forge remote is a bare repo on this host; InitBareRepo pins the
	// author, committer and both dates so its head SHA is a constant.
	repoDir := t.TempDir()
	headSHA, err := fake.InitBareRepo(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	heads := map[uuid.UUID]string{}
	runsSvc := runs.NewService(pool, clk, rec,
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		fake.Forge{Heads: heads}, runs.Params{Snapshot: runs.DefaultSnapshot()})

	// The admin sets the stage through the real services.
	project, err := assetsSvc.CreateProject(ctx, admin, "runner-e2e", "")
	if err != nil {
		t.Fatal(err)
	}
	modelCred, err := assetsSvc.CreateCredential(ctx, admin, "anthropic", assets.CredentialModelKey, []byte("sk-ant-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := assetsSvc.CreateCredential(ctx, admin, "github-bot", assets.CredentialForgeToken, []byte("ghp-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assetsSvc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "cc", Kind: assets.HarnessClaudeCode, ImageRef: e2eGitImage,
		ProviderEndpoint: "http://localhost:1", CredentialID: modelCred.ID,
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := assetsSvc.CreateRepo(ctx, admin, project.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "runner/e2e", DefaultBranch: "main", CredentialID: forgeCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	heads[repo.ID] = headSHA

	wf, err := workflowsSvc.CreateWorkflow(ctx, admin, "runner-e2e", "")
	if err != nil {
		t.Fatal(err)
	}
	doc := `{
	  "schema": 1,
	  "nodes": [
	    {"id": "impl", "type": "implement", "harness": "cc", "instructions": "Implement the change."}
	  ],
	  "edges": []
	}`
	version, _, err := workflowsSvc.CreateVersion(ctx, admin, wf.ID, []byte(doc))
	if err != nil {
		t.Fatal(err)
	}

	// The agent script the resolver injects: write a uniquely-named file so
	// the extract container's diff (the patch) is unambiguous.
	const scriptedFile = "RUNNER_E2E.md"
	script := "set -e\necho hello-from-e2e-agent\necho scripted > /workspace/" + scriptedFile + "\n"

	dataRoot := t.TempDir()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	egressNet := "caesar-egress-e2e-" + uuid.NewString()[:8]
	rnr, err := runner.New(engine,
		scriptedResolver{inner: appHarnessResolver{svc: assetsSvc}, script: script},
		appCredentialOpener{svc: assetsSvc},
		localBareGit{dir: repoDir},
		runsSvc,
		runner.Options{
			DataRoot: dataRoot, CaesarImage: e2eCaesarImage, GitImage: e2eGitImage,
			EgressNetwork: egressNet,
			// Tight ceilings so a runaway costs seconds; a low watermark so
			// admission's free-space statfs on DataRoot passes on any disk.
			WorkspaceQuotaBytes: 64 << 20, WorkspaceQuotaFiles: 2000,
			LogCapBytes: 256 << 10, LogFloodBytes: 1 << 20,
			FreeBytesWatermark: 1 << 20, QuotaPollInterval: 200 * time.Millisecond,
		}, log)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}

	// The runner does NOT create DataRoot (buildRunner does at wiring time);
	// this test constructs the runner directly, so it mirrors that step —
	// otherwise AdmitClaim's statfs on a missing DataRoot refuses the claim.
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	started := time.Now().Unix()
	t.Cleanup(func() { e2eSweep(t, engine, egressNet, started) })

	orch := runs.NewOrchestrator(runsSvc, rnr, rnr, runs.OrchestratorConfig{
		Workers: 1, PollInterval: 50 * time.Millisecond, LeaseTTL: 60 * time.Second,
		HeartbeatInterval: 10 * time.Second, OutboxRetention: 168 * time.Hour,
	}, log)
	orchCtx, orchCancel := context.WithCancel(ctx)
	orchDone := make(chan struct{})
	go func() { defer close(orchDone); _ = orch.Run(orchCtx) }()
	t.Cleanup(func() { orchCancel(); <-orchDone })

	_, run, err := runsSvc.CreateTask(ctx, admin, runs.NewTask{
		RepoID: repo.ID, WorkflowID: wf.ID, WorkflowVersion: version.Version, Title: "runner e2e",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Assertion 1: the run reaches succeeded, driven through a real container.
	var finalRun runs.Run
	var phases []runs.Phase
	var arts []runs.ArtifactMeta
	deadline := time.Now().Add(10 * time.Minute)
	for {
		finalRun, phases, arts, err = runsSvc.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finalRun.Status == runs.RunSucceeded || finalRun.Status == runs.RunFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish within 10m; status = %s, outcome = %+v", finalRun.Status, finalRun.Outcome)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if finalRun.Status != runs.RunSucceeded {
		t.Fatalf("run status = %s, outcome = %+v", finalRun.Status, finalRun.Outcome)
	}

	// Assertion 2: a patch artifact whose diff contains the scripted file.
	byType := map[string]runs.ArtifactMeta{}
	for _, a := range arts {
		byType[a.Type] = a
	}
	if _, ok := byType[runs.ArtifactKindPatch]; !ok {
		t.Fatalf("no patch artifact in %+v", arts)
	}
	patchPayload := e2eArtifactPayload(t, pool, run.ID, runs.ArtifactKindPatch)
	encoded, ok := patchPayload["diff"].(string)
	if !ok {
		t.Fatalf("patch payload carries no diff string: %v", patchPayload)
	}
	diff, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("patch diff is not base64: %v", err)
	}
	if !strings.Contains(string(diff), scriptedFile) {
		t.Errorf("patch diff does not mention %s:\n%s", scriptedFile, diff)
	}
	if patchPayload["base_sha"] != headSHA {
		t.Errorf("patch base_sha = %v, want %s", patchPayload["base_sha"], headSHA)
	}

	// Assertion 3: a log_ref artifact, and its agent log is on disk.
	if _, ok := byType[runs.ArtifactKindLogRef]; !ok {
		t.Fatalf("no log_ref artifact in %+v", arts)
	}
	logRef := e2eArtifactPayload(t, pool, run.ID, runs.ArtifactKindLogRef)
	streams, ok := logRef["streams"].(map[string]any)
	if !ok {
		t.Fatalf("log_ref carries no streams: %v", logRef)
	}
	agent, ok := streams["agent"].(map[string]any)
	if !ok {
		t.Fatalf("log_ref carries no agent stream: %v", streams)
	}
	agentRel, _ := agent["path"].(string)
	agentLog, err := os.ReadFile(filepath.Join(dataRoot, agentRel))
	if err != nil {
		t.Fatalf("agent log not on disk at %s: %v", agentRel, err)
	}
	if !strings.Contains(string(agentLog), "hello-from-e2e-agent") {
		t.Errorf("agent stdout not captured: %q", agentLog)
	}

	// Assertion 4: phase.started and phase.succeeded for the node.
	events, err := runsSvc.ListRunEvents(ctx, run.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Kind] = true
	}
	if !seen[runs.EventPhaseStarted] {
		t.Errorf("missing %s event; saw %v", runs.EventPhaseStarted, kinds(events))
	}
	if !seen[runs.EventPhaseSucceeded] {
		t.Errorf("missing %s event; saw %v", runs.EventPhaseSucceeded, kinds(events))
	}
	// The node reaches a succeeded phase. Transient container failures under
	// load may cost earlier attempts; the run's terminal success guarantees a
	// succeeded terminal phase, and that is what this asserts (not attempt 1).
	var implSucceeded bool
	for _, p := range phases {
		if p.NodeID == "impl" && p.Status == runs.PhaseSucceeded {
			implSucceeded = true
		}
	}
	if !implSucceeded {
		t.Errorf("no succeeded impl phase in %+v", phases)
	}

	// Assertion 5: an audit row records the credential use (UseCredential
	// path) — the runner opened the harness's model credential per attempt.
	var credUses int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_entries WHERE action = 'credential.used' AND subject_id = $1`,
		modelCred.ID.String()).Scan(&credUses); err != nil {
		t.Fatal(err)
	}
	if credUses == 0 {
		t.Errorf("no credential.used audit row for the harness model credential %s", modelCred.ID)
	}

	// Assertion 6: no per-attempt caesar.managed containers or networks
	// survive. The shared egress network is infra the runner reuses across
	// attempts (its own sweep spares it too); this test tears it down in
	// cleanup and excludes it here.
	survivors, err := engine.ContainerListByLabel(ctx, "caesar.managed=true")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range survivors {
		if c.Created >= started {
			t.Errorf("caesar.managed container survived: %s (%v)", c.ID, c.Names)
		}
	}
	nets, err := engine.NetworkListByLabel(ctx, "caesar.managed=true")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nets {
		if n.Name == egressNet {
			continue
		}
		if created, perr := time.Parse(time.RFC3339Nano, n.Created); perr == nil && created.Unix() >= started {
			t.Errorf("caesar.managed network survived: %s", n.Name)
		}
	}
}

func kinds(events []runs.RunEventRecord) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// e2eArtifactPayload reads one artifact's payload off the row (payloads are
// not on the service surface).
func e2eArtifactPayload(t *testing.T, pool *pgxpool.Pool, runID uuid.UUID, artType string) map[string]any {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT payload FROM artifacts WHERE run_id = $1 AND type = $2`, runID, artType).Scan(&raw); err != nil {
		t.Fatalf("read %s payload: %v", artType, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal %s payload: %v", artType, err)
	}
	return payload
}

// e2eSweep removes what the attempt left behind: caesar.managed containers
// created since the test started, any per-attempt networks, and the shared
// egress network the runner's own teardown does not own.
func e2eSweep(t *testing.T, engine *dockerapi.Client, egressNet string, since int64) {
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
	for range 10 {
		if err := engine.NetworkRemove(ctx, egressNet); err == nil || dockerapi.IsNotFound(err) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("egress network %s survived the sweep", egressNet)
}
