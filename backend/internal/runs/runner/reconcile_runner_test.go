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

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// stageAttemptForInspect drives the pipeline's own steps up to a staged
// workspace — the state a worker is in when it dies mid-attempt. It calls the
// unexported production steps rather than a test hook: the point of the
// exercise is that reconciliation meets containers the real path created.
func stageAttemptForInspect(t *testing.T, r *Runner, ref runs.AttemptRef) RunSpec {
	t.Helper()
	ctx := t.Context()
	if err := os.MkdirAll(r.attemptDir(ref), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.logsDir(ref), 0o700); err != nil {
		t.Fatal(err)
	}
	req := implementRequest(t, ref, fixtureSHA(t))
	hs, err := r.harnesses.HarnessSpec(ctx, req.HarnessID)
	if err != nil {
		t.Fatalf("harness spec: %v", err)
	}
	spec, err := r.buildSpec(ctx, req, hs)
	if err != nil {
		t.Fatalf("build spec: %v", err)
	}
	clone, err := r.git.CloneSpec(ctx, req.Repo)
	if err != nil {
		t.Fatalf("clone spec: %v", err)
	}
	if err := r.ensureNetworks(ctx, ref); err != nil {
		t.Fatalf("ensure networks: %v", err)
	}
	if err := r.stageAttempt(ctx, ref, spec, clone); err != nil {
		t.Fatalf("stage: %v", err)
	}
	return spec
}

func TestInspect_NoContainerReportsLost(t *testing.T) {
	r := newDockerRunner(t, Options{}, scriptedHarness("", "true"), &recordingSink{})
	ref := dockerRef()

	res, err := r.Inspect(t.Context(), ref)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if res.Completed {
		t.Errorf("an attempt with no container was adopted: %+v", res)
	}
	if len(res.Result.Artifacts) != 0 {
		t.Errorf("nothing ran, yet artifacts were produced: %v", res.Result.Artifacts)
	}
}

func TestInspect_CompletedContainerIsAdopted(t *testing.T) {
	script := `
set -e
echo hello-from-adopted-agent
echo "adopted content" > /workspace/NEW.md
mkdir -p /workspace/.caesar/out
cat > /workspace/.caesar/out/artifacts.json <<'EOF'
[{"type":"plan","payload":{"content":"adopted plan"}}]
EOF
`
	r := newDockerRunner(t, Options{}, scriptedHarness("", script), &recordingSink{})
	ref := dockerRef()

	// The crash window: the agent finished and the worker died before it
	// could collect, so nothing has torn anything down.
	spec := stageAttemptForInspect(t, r, ref)
	d, err := driverFor(spec.Harness.Kind)
	if err != nil {
		t.Fatalf("driver for %s: %v", spec.Harness.Kind, err)
	}
	agent, err := r.runAgentToCompletion(t.Context(), ref, spec, d)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if agent.exitCode != 0 {
		t.Fatalf("agent exited %d, want a completed attempt to adopt", agent.exitCode)
	}

	res, err := r.Inspect(t.Context(), ref)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !res.Completed {
		t.Fatalf("a finished attempt with an intact workspace was not adopted: %+v", res)
	}

	byType := artifactsByType(res.Result.Artifacts)
	patch, ok := byType[runs.ArtifactKindPatch]
	if !ok {
		t.Fatalf("adoption produced no patch: %v", res.Result.Artifacts)
	}
	if diff := decodedDiff(t, patch.Payload); !strings.Contains(diff, "NEW.md") {
		t.Errorf("adopted patch = %q, want the agent's file", diff)
	}
	if patch.Payload["base_sha"] != fixtureSHA(t) {
		t.Errorf("adopted patch base_sha = %v, want %s", patch.Payload["base_sha"], fixtureSHA(t))
	}
	if got := byType[runs.ArtifactKindPlan].Payload["content"]; got != "adopted plan" {
		t.Errorf("adoption lost the declared artifact: %v", got)
	}
	if _, ok := byType[runs.ArtifactKindLogRef]; !ok {
		t.Errorf("adoption produced no log_ref: %v", res.Result.Artifacts)
	}

	// Adoption owns the teardown the dead worker never ran — logs excepted.
	assertNoContainer(t, r, agentName(ref))
	assertNoContainer(t, r, gatewayName(ref))
	assertNoContainer(t, r, extractName(ref))
	assertNoNetwork(t, r, networkName(ref))
	if _, err := os.Stat(r.attemptDir(ref)); !os.IsNotExist(err) {
		t.Errorf("attempt dir survived adoption, stat err = %v", err)
	}
	if log := readLogFile(t, r, ref, agentLogFile); !strings.Contains(log, "hello-from-adopted-agent") {
		t.Errorf("the adopted attempt's log did not survive: %q", log)
	}
}

// A worker that died after its agent had already hit the cap must not lose
// that answer. The attempt is adopted as the typed failure the harness
// reported — reset and all — because reporting it lost would charge a cap
// wait to the node's failure budget. Reading the exit takes the manifest: it
// is what says which harness's markers apply, and without one adoption has to
// fall back to the pre-driver answer.
func TestInspect_NonzeroExitIsAdoptedWithItsTypedOutcome(t *testing.T) {
	reset := time.Now().Add(45 * time.Minute).UTC().Truncate(time.Second)
	script := fmt.Sprintf("echo 'Claude AI usage limit reached|%d'; exit 1", reset.Unix())

	capHitAttempt := func(t *testing.T, withManifest bool) runs.InspectResult {
		t.Helper()
		r := newDockerRunner(t, Options{}, scriptedHarness("", script), &recordingSink{})
		ref := dockerRef()
		spec := stageAttemptForInspect(t, r, ref)
		if withManifest {
			if err := r.writeManifest(ref, attemptManifest{
				CredentialID: uuid.NewString(), Kind: "model_key",
				Custody: spec.Custody, HarnessKind: spec.Harness.Kind,
			}); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
		}
		d, err := driverFor(spec.Harness.Kind)
		if err != nil {
			t.Fatalf("driver for %s: %v", spec.Harness.Kind, err)
		}
		agent, err := r.runAgentToCompletion(t.Context(), ref, spec, d)
		if err != nil {
			t.Fatalf("run agent: %v", err)
		}
		if agent.exitCode != 1 {
			t.Fatalf("agent exited %d, want the scripted 1", agent.exitCode)
		}
		res, err := r.Inspect(t.Context(), ref)
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		return res
	}

	t.Run("with a manifest the marker table applies", func(t *testing.T) {
		res := capHitAttempt(t, true)
		if !res.Completed || res.Outcome == nil {
			t.Fatalf("result = %+v, want an adopted typed outcome", res)
		}
		if res.Outcome.Code != runs.CodeRateLimited {
			t.Errorf("code = %q, want rate_limited", res.Outcome.Code)
		}
		if res.Outcome.RetryAt == nil || !res.Outcome.RetryAt.Equal(reset) {
			t.Errorf("RetryAt = %v, want the marker's own %s", res.Outcome.RetryAt, reset)
		}
		// An adopted failure owes the same evidence the worker path attaches
		// to its own: without the log pointer, a run that died in
		// reconciliation leaves nothing to read.
		byType := artifactsByType(res.Result.Artifacts)
		logRef, ok := byType[runs.ArtifactKindLogRef]
		if !ok {
			t.Fatalf("adopted failure carries no log_ref: %v", res.Result.Artifacts)
		}
		if logRef.Payload["streams"] == nil {
			t.Errorf("log_ref payload = %v, want the captured streams", logRef.Payload)
		}
		// The failure snapshot belongs to the worker path until adoption can
		// rebuild the redaction set (Task 11): an unredacted log tail in a
		// stored artifact is a leaked credential.
		if _, ok := byType[runs.ArtifactKindFailureSnapshot]; ok {
			t.Error("adoption produced a failure snapshot it cannot redact")
		}
	})

	t.Run("without one the markers are unreadable", func(t *testing.T) {
		// The manifest is the route back to the driver, so its absence costs
		// the meaning of the exit — not the attempt. The failure is adopted,
		// it just reads as the generic exit it looks like from outside.
		res := capHitAttempt(t, false)
		if !res.Completed || res.Outcome == nil {
			t.Fatalf("result = %+v, want the failure adopted anyway", res)
		}
		if res.Outcome.Code != runs.CodeHarnessError {
			t.Errorf("code = %q, want the pre-driver harness_error", res.Outcome.Code)
		}
		if res.Outcome.RetryAt != nil {
			t.Errorf("RetryAt = %v, want nil: nothing read the cap marker", res.Outcome.RetryAt)
		}
	})
}

func TestInspect_RunningContainerIsKilledThenInspected(t *testing.T) {
	r := newDockerRunner(t, Options{}, scriptedHarness("", "sleep 600"), &recordingSink{})
	ref := dockerRef()

	spec := stageAttemptForInspect(t, r, ref)
	if _, err := r.startAgent(t.Context(), ref, spec); err != nil {
		t.Fatalf("start agent: %v", err)
	}
	waitForStatus(t, r, agentName(ref), "running")

	started := time.Now()
	res, err := r.Inspect(t.Context(), ref)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if res.Completed {
		t.Fatalf("a container still running was adopted: %+v", res)
	}
	// A sleeping agent does not stop on its own; the clock is the proof the
	// kill landed rather than the sleep running out.
	if elapsed := time.Since(started); elapsed > 2*time.Minute {
		t.Errorf("kill-then-inspect took %v", elapsed)
	}
	assertNoContainer(t, r, agentName(ref))
	assertNoNetwork(t, r, networkName(ref))
	if _, err := os.Stat(r.attemptDir(ref)); !os.IsNotExist(err) {
		t.Errorf("attempt dir survived reconciliation, stat err = %v", err)
	}
}

func TestSweepOrphans_RemovesOnlyOldLabeledResources(t *testing.T) {
	r := newDockerRunner(t, Options{}, scriptedHarness("", "true"), &recordingSink{})
	ctx := t.Context()

	// Created stamps cannot be faked against a real daemon, so the age
	// predicate is proved in both directions instead: an olderThan of zero
	// makes everything old enough, an olderThan of a day makes nothing so.
	refs := []runs.AttemptRef{dockerRef(), dockerRef()}
	create := func() {
		t.Helper()
		for _, ref := range refs {
			createLabeledCorpse(t, r, ref)
			if _, err := r.engine.NetworkCreate(ctx, networkName(ref), true, labels(ref)); err != nil {
				t.Fatalf("create network: %v", err)
			}
		}
	}
	create()
	// A container nobody labeled is nobody's orphan, in either sweep.
	bystander := createUnlabeledBystander(t, r)

	if err := r.SweepOrphans(ctx, 0); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, ref := range refs {
		assertNoContainer(t, r, agentName(ref))
		assertNoNetwork(t, r, networkName(ref))
	}
	assertContainer(t, r, bystander)

	create()
	// The dirs half of the sweep answers to mtime, and must answer in both
	// directions too.
	attempts := filepath.Join(r.opts.DataRoot, "attempts")
	stale, fresh := filepath.Join(attempts, "stale"), filepath.Join(attempts, "fresh")
	for _, dir := range []string{stale, fresh} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if err := r.SweepOrphans(ctx, 24*time.Hour); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, ref := range refs {
		assertContainer(t, r, agentName(ref))
		if _, err := r.engine.NetworkInspect(ctx, networkName(ref)); err != nil {
			t.Errorf("network %s was swept before its time: %v", networkName(ref), err)
		}
	}
	assertContainer(t, r, bystander)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a 48h-old attempt dir survived the sweep, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh attempt dir was swept: %v", err)
	}
}

// createLabeledCorpse leaves behind exactly what a killed worker does: an
// exited container carrying the attempt's labels and its name.
func createLabeledCorpse(t *testing.T, r *Runner, ref runs.AttemptRef) {
	t.Helper()
	ctx := t.Context()
	id, err := r.engine.ContainerCreate(ctx, agentName(ref), dockerapi.ContainerConfig{
		Image:  gitImage,
		Cmd:    []string{"/bin/true"},
		User:   r.runAs(),
		Labels: labels(ref),
	}, r.hardenedHost(nil, noNetwork, gitMemoryBytes, gitNanoCPUs, gitPidsLimit), nil)
	if err != nil {
		t.Fatalf("create corpse: %v", err)
	}
	if err := r.engine.ContainerStart(ctx, id); err != nil {
		t.Fatalf("start corpse: %v", err)
	}
	if _, err := r.engine.ContainerWait(ctx, id); err != nil {
		t.Fatalf("wait corpse: %v", err)
	}
}

func createUnlabeledBystander(t *testing.T, r *Runner) string {
	t.Helper()
	ctx := t.Context()
	name := "caesar-sweep-bystander-" + uuid.NewString()[:8]
	id, err := r.engine.ContainerCreate(ctx, name, dockerapi.ContainerConfig{
		Image: gitImage,
		Cmd:   []string{"sleep", "600"},
		User:  r.runAs(),
	}, r.hardenedHost(nil, noNetwork, gitMemoryBytes, gitNanoCPUs, gitPidsLimit), nil)
	if err != nil {
		t.Fatalf("create bystander: %v", err)
	}
	t.Cleanup(func() { _ = r.engine.ContainerRemove(t.Context(), id, true) })
	if err := r.engine.ContainerStart(ctx, id); err != nil {
		t.Fatalf("start bystander: %v", err)
	}
	return name
}

func assertContainer(t *testing.T, r *Runner, name string) {
	t.Helper()
	if _, err := r.engine.ContainerInspect(t.Context(), name); err != nil {
		t.Errorf("container %s is gone: %v", name, err)
	}
}

// waitForStatus blocks until a container reaches one state, so a test that
// means to catch it running never races the daemon's bookkeeping.
func waitForStatus(t *testing.T, r *Runner, name, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		insp, err := r.engine.ContainerInspect(t.Context(), name)
		if err == nil && insp.State.Status == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("container %s never reached status %q", name, want)
}
