//go:build runnerintegration

package runner

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// assertFailureArtifacts checks the contract every post-agent-start failure
// owes the orchestrator (Task 1): the typed error carries the evidence, so a
// terminal attempt is still explainable after teardown has removed the
// containers and the workspace.
func assertFailureArtifacts(t *testing.T, res runs.ExecutionResult) map[string]any {
	t.Helper()
	byType := artifactsByType(res.Artifacts)
	if _, ok := byType[runs.ArtifactKindLogRef]; !ok {
		t.Fatalf("no log_ref artifact in %v", res.Artifacts)
	}
	snap, ok := byType[runs.ArtifactKindFailureSnapshot]
	if !ok {
		t.Fatalf("no failure_snapshot artifact in %v", res.Artifacts)
	}
	return snap.Payload
}

func TestExecute_WorkspaceFillIsQuotaKilled(t *testing.T) {
	script := `i=0; while true; do dd if=/dev/zero of=/workspace/f$i bs=1M count=1 2>/dev/null; i=$((i+1)); done`
	sink := &recordingSink{}
	r := newDockerRunner(t, Options{
		WorkspaceQuotaBytes: 8 << 20,
		WorkspaceQuotaFiles: 2000,
		QuotaPollInterval:   200 * time.Millisecond,
	}, scriptedHarness("", script), sink)
	ref := dockerRef()

	started := time.Now()
	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))

	var oe runs.OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want a typed outcome", err)
	}
	if oe.Outcome.Code != runs.CodeWorkspaceQuotaExceeded || oe.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("outcome = %+v, want a permanent workspace_quota_exceeded", oe.Outcome)
	}
	// An agent that is only asked to stop would keep writing; the quota is
	// enforced by killing it, so the attempt ends in seconds.
	if elapsed := time.Since(started); elapsed > 2*time.Minute {
		t.Errorf("the quota kill took %v", elapsed)
	}

	snap := assertFailureArtifacts(t, res)
	ws, ok := snap["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot workspace = %v", snap["workspace"])
	}
	if files, _ := ws["files"].(int64); files < 2 {
		t.Errorf("snapshot listed %v files, want the fill it died of: %v", ws["files"], ws)
	}
	assertNoContainer(t, r, agentName(ref))
	assertNoContainer(t, r, gatewayName(ref))
	assertNoNetwork(t, r, networkName(ref))
}

func TestExecute_LogFloodTruncatesThenKills(t *testing.T) {
	script := `while true; do echo "` + strings.Repeat("A", 512) + `"; done`
	sink := &recordingSink{}
	r := newDockerRunner(t, Options{
		LogCapBytes:   256 << 10,
		LogFloodBytes: 1 << 20,
	}, scriptedHarness("", script), sink)
	ref := dockerRef()

	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))

	var oe runs.OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want a typed outcome", err)
	}
	if oe.Outcome.Code != runs.CodeLogFlood || oe.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("outcome = %+v, want a permanent log_flood", oe.Outcome)
	}

	// The file stops at the cap while the kill answers the volume behind it.
	if got := int64(len(readLogFile(t, r, ref, agentLogFile))); got != r.opts.LogCapBytes {
		t.Errorf("agent.log = %d bytes, want exactly the cap %d", got, r.opts.LogCapBytes)
	}
	var truncations []sinkEvent
	for _, e := range sink.events() {
		if e.Kind == "phase.log_truncated" && e.Payload["stream"] == "agent" {
			truncations = append(truncations, e)
		}
	}
	if len(truncations) != 1 {
		t.Fatalf("phase.log_truncated for the agent fired %d times, want once: %v", len(truncations), truncations)
	}
	if truncations[0].Payload["cap_bytes"] != r.opts.LogCapBytes {
		t.Errorf("truncation payload = %v, want the cap", truncations[0].Payload)
	}

	snap := assertFailureArtifacts(t, res)
	if tail, _ := snap["log_tail"].(string); !strings.Contains(tail, "AAAA") {
		t.Errorf("log_tail carries none of what the agent produced: %.80q", tail)
	}
	assertNoContainer(t, r, agentName(ref))
}

func TestExecute_DeadlineKillClassifiesTimeout(t *testing.T) {
	r := newDockerRunner(t, Options{}, scriptedHarness("", "sleep 600"), &recordingSink{})
	ref := dockerRef()

	// The deadline has to clear staging — a real clone in a real container —
	// and then land on the agent while it is sleeping.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	started := time.Now()
	res, err := r.Execute(ctx, implementRequest(t, ref, fixtureSHA(t)))
	elapsed := time.Since(started)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline surfaced so ClassifyOutcome maps transient/timeout", err)
	}
	if out := runs.ClassifyOutcome(err); out.Class != runs.OutcomeTransient || out.Code != runs.CodeTimeout {
		t.Errorf("classified as %+v, want transient/timeout", out)
	}
	// A sleeping agent does not stop on its own: the wall clock is the proof
	// the kill actually landed rather than the sleep running out.
	if elapsed > 60*time.Second {
		t.Errorf("the deadline kill took %v", elapsed)
	}
	// The snapshot describes a corpse, not a container caught mid-kill.
	snap := assertFailureArtifacts(t, res)
	if snap["status"] != "exited" || snap["exit_code"] == int64(0) {
		t.Errorf("snapshot state = status:%v exit_code:%v, want a killed container",
			snap["status"], snap["exit_code"])
	}
	assertNoContainer(t, r, agentName(ref))
	assertNoContainer(t, r, gatewayName(ref))
	assertNoNetwork(t, r, networkName(ref))
}

func TestExecute_EgressDenialsBecomeCappedEvents(t *testing.T) {
	// Thirty refused tunnels, spoken straight at the gateway: no allowlist
	// entry and no proxy credential, so every one is denied at the door.
	const attempts = 30
	script := `i=0
while [ $i -lt ` + strconv.Itoa(attempts) + ` ]; do
  printf 'CONNECT denied.example:443 HTTP/1.1\r\nHost: denied.example:443\r\nConnection: close\r\n\r\n' | nc egress 8080 >/dev/null 2>&1
  i=$((i+1))
done
exit 0`
	sink := &recordingSink{}
	r := newDockerRunner(t, Options{}, scriptedHarness("", script), sink)
	ref := dockerRef()

	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	if err != nil {
		t.Fatalf("execute: %v\ngateway log:\n%s", err, readLogFile(t, r, ref, gatewayLogFile))
	}

	var denials []sinkEvent
	for _, e := range sink.events() {
		if e.Kind == "phase.egress_denied" {
			denials = append(denials, e)
		}
	}
	// The event stream is a narrative, not a metric: it stops at the cap.
	if len(denials) != maxDenialEvents {
		t.Fatalf("phase.egress_denied fired %d times, want the cap of %d\ngateway log:\n%s",
			len(denials), maxDenialEvents, readLogFile(t, r, ref, gatewayLogFile))
	}
	for _, d := range denials {
		if d.Payload["host"] != "denied.example" || d.Payload["reason"] == "" {
			t.Fatalf("denial payload = %v, want the pinned host/reason keys", d.Payload)
		}
	}

	// The meter, by contrast, counted every one of them.
	egress, ok := artifactsByType(res.Artifacts)[runs.ArtifactKindLogRef].Payload["egress"].(map[string]any)
	if !ok {
		t.Fatalf("log_ref has no egress map: %v", res.Artifacts)
	}
	if egress["parse_error"] != nil {
		t.Fatalf("the attempt's egress bill was not harvested: %v\ngateway log:\n%s",
			egress, readLogFile(t, r, ref, gatewayLogFile))
	}
	if denied, _ := egress["denied"].(int64); denied < attempts {
		t.Errorf("egress.denied = %v, want at least the %d refusals", egress["denied"], attempts)
	}
}
