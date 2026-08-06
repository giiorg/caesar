//go:build runnerintegration

package runner

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestExecute_HappyPath_PatchDeclaredArtifactsAndLifecycle(t *testing.T) {
	script := `
set -e
echo hello-from-agent
echo "artifact content" > /workspace/NEW.md
mkdir -p /workspace/.caesar/out
cat > /workspace/.caesar/out/artifacts.json <<'EOF'
[{"type":"plan","payload":{"content":"scripted plan"}}]
EOF
`
	sink := &recordingSink{}
	r := newDockerRunner(t, Options{}, scriptedHarness("", script), sink, nil)
	ref := dockerRef()

	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	if err != nil {
		t.Fatal(err)
	}

	byType := artifactTypes(res.Artifacts)
	patch, ok := byType[runs.ArtifactKindPatch]
	if !ok {
		t.Fatalf("no patch artifact in %v", res.Artifacts)
	}
	diff := decodedDiff(t, patch.Payload)
	if !strings.Contains(diff, "NEW.md") {
		t.Errorf("patch diff = %q", diff)
	}
	if patch.Payload["base_sha"] != fixtureSHA(t) {
		t.Errorf("patch base_sha = %v, want %s", patch.Payload["base_sha"], fixtureSHA(t))
	}
	if patch.Payload["tree_hash"] == "" || patch.Payload["tree_hash"] == nil {
		t.Error("patch carries no tree hash")
	}
	if got := byType[runs.ArtifactKindPlan].Payload["content"]; got != "scripted plan" {
		t.Errorf("declared artifact lost: %v", got)
	}

	logRef := byType[runs.ArtifactKindLogRef].Payload
	if logRef["egress"] == nil {
		t.Error("log_ref missing the egress key")
	}
	if agentLog := readLogFile(t, r, ref, agentLogFile); !strings.Contains(agentLog, "hello-from-agent") {
		t.Errorf("agent stdout not captured: %q", agentLog)
	}
	if gwLog := readLogFile(t, r, ref, gatewayLogFile); !strings.Contains(gwLog, "egress.summary") {
		t.Errorf("gateway did not flush its usage summary: %q", gwLog)
	}

	// Nothing the attempt created outlives it, except the logs.
	assertNoContainer(t, r, agentName(ref))
	assertNoContainer(t, r, gatewayName(ref))
	assertNoContainer(t, r, stageName(ref))
	assertNoContainer(t, r, extractName(ref))
	assertNoNetwork(t, r, networkName(ref))
	if _, err := os.Stat(r.attemptDir(ref)); !os.IsNotExist(err) {
		t.Errorf("attempt dir must be removed, stat err = %v", err)
	}
}

func TestExecute_BrokeredProviderCallNeverShowsTheAgentTheCredential(t *testing.T) {
	endpoint, seen := startFakeProvider(t)
	script := `set -e
wget -qO /workspace/models.json --header "x-api-key: $CAESAR_BROKER_TOKEN" "$CAESAR_BROKER_URL/v1/models"
`
	r := newDockerRunner(t, Options{}, scriptedHarness(endpoint, script), &recordingSink{}, nil)
	ref := dockerRef()

	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	if err != nil {
		t.Fatalf("execute: %v\nagent log:\n%s", err, readLogFile(t, r, ref, agentLogFile))
	}

	got := seen.snapshot()
	if got.requests != 1 {
		t.Fatalf("provider saw %d requests, want exactly 1", got.requests)
	}
	if got.apiKeys[0] != "real-provider-key" {
		t.Errorf("provider was presented %q, want the brokered credential", got.apiKeys[0])
	}
	if got.paths[0] != "/v1/models" {
		t.Errorf("provider path = %q", got.paths[0])
	}
	// The attempt token authenticates the agent to the gateway and must stop
	// there: upstream must never see anything the sandbox chose.
	for _, v := range append(got.apiKeys, got.authzs...) {
		if v != "" && v != "real-provider-key" {
			t.Errorf("upstream saw a sandbox-chosen credential %q", v)
		}
	}

	patch := artifactTypes(res.Artifacts)[runs.ArtifactKindPatch].Payload
	diff := decodedDiff(t, patch)
	if !strings.Contains(diff, "models.json") || !strings.Contains(diff, "claude-fable-5") {
		t.Errorf("brokered response never reached the workspace: %q", diff)
	}

	// The credential lives in the gateway's environment and nowhere that
	// outlives the attempt: not in either captured stream, not in the patch.
	if strings.Contains(diff, "real-provider-key") {
		t.Error("the provider credential reached the patch")
	}
	for _, name := range []string{agentLogFile, gatewayLogFile} {
		if strings.Contains(readLogFile(t, r, ref, name), "real-provider-key") {
			t.Errorf("%s contains the provider credential", name)
		}
	}
}

func TestExecute_AgentNonzeroExitClassifiesHarnessError(t *testing.T) {
	r := newDockerRunner(t, Options{}, scriptedHarness("", "echo failing; exit 3"), &recordingSink{}, nil)
	ref := dockerRef()

	_, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	var oe runs.OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want a typed outcome", err)
	}
	if oe.Outcome.Class != runs.OutcomeTransient || oe.Outcome.Code != runs.CodeHarnessError {
		t.Errorf("outcome = %+v", oe.Outcome)
	}
	if !strings.Contains(oe.Outcome.Message, "3") {
		t.Errorf("message does not carry the exit code: %q", oe.Outcome.Message)
	}
	assertNoContainer(t, r, agentName(ref))
	assertNoNetwork(t, r, networkName(ref))
}

func TestExecute_StageReportOutcomePropagatesAndNoAgentRuns(t *testing.T) {
	sink := &recordingSink{}
	r := newDockerRunner(t, Options{}, scriptedHarness("", "echo the agent must never run"), sink, nil)
	ref := dockerRef()

	// A base SHA the fixture remote does not carry: staging refuses before
	// any workspace exists, so no agent is ever handed one.
	_, err := r.Execute(t.Context(), implementRequest(t, ref, strings.Repeat("a", 40)))
	var oe runs.OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want a typed outcome", err)
	}
	if oe.Outcome.Class != runs.OutcomePermanent || oe.Outcome.Code != runs.CodeBaseSHAUnavailable {
		t.Fatalf("outcome = %+v", oe.Outcome)
	}
	assertNoContainer(t, r, agentName(ref))
	assertNoContainer(t, r, gatewayName(ref))
	assertNoNetwork(t, r, networkName(ref))
	if _, err := os.Stat(r.logsDir(ref) + "/" + agentLogFile); !os.IsNotExist(err) {
		t.Errorf("an agent that never ran left a log: %v", err)
	}
	if evs := sink.events(); len(evs) != 0 {
		t.Errorf("a failed staging emitted runtime events: %v", evs)
	}
}

// The marker table, end to end against a real container: a harness that hit
// its cap exits 1 like every other failure, and only its own output says
// otherwise — including the moment the window reopens, which the retry has to
// wait for rather than guess at.
func TestExecute_RateLimitMarkerCarriesTheProviderReset(t *testing.T) {
	reset := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	script := fmt.Sprintf("echo 'Claude AI usage limit reached|%d'; exit 1", reset.Unix())
	r := newDockerRunner(t, Options{}, scriptedHarness("", script), &recordingSink{}, nil)
	ref := dockerRef()

	_, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	var oe runs.OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want a typed outcome", err)
	}
	if oe.Outcome.Class != runs.OutcomeTransient || oe.Outcome.Code != runs.CodeRateLimited {
		t.Fatalf("outcome = %+v, want a transient rate_limited", oe.Outcome)
	}
	if oe.Outcome.RetryAt == nil || !oe.Outcome.RetryAt.Equal(reset) {
		t.Errorf("RetryAt = %v, want the marker's own %s", oe.Outcome.RetryAt, reset)
	}
	// A cap window is a fact of subscription life, not the node's failure.
	if oe.Outcome.Chargeable() {
		t.Error("a cap wait charged the node's failure budget")
	}
}

// A credential its own provider rejected fails fast and says which one: the
// attempt's outcome names it, and the aggregate is told, so the next claimant
// does not spend another attempt rediscovering the same dead token.
func TestExecute_AuthExpiredMarkerNamesAndMarksTheCredential(t *testing.T) {
	// A brokered attempt takes no lease, but the freshness signal still goes
	// to the aggregate — so this case is one of the few that wires one.
	leaser := &fakeLeaser{}
	r := newDockerRunner(t, Options{},
		scriptedHarness("", "echo 'Invalid API key - Please run /login'; exit 1"), &recordingSink{}, leaser)
	ref := dockerRef()
	req := implementRequest(t, ref, fixtureSHA(t))

	_, err := r.Execute(t.Context(), req)
	var oe runs.OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want a typed outcome", err)
	}
	if oe.Outcome.Class != runs.OutcomePermanent || oe.Outcome.Code != runs.CodeAuthExpired {
		t.Fatalf("outcome = %+v, want a permanent auth_expired", oe.Outcome)
	}
	if !strings.Contains(oe.Outcome.Message, req.Credential.ID.String()) {
		t.Errorf("message = %q, want it to name credential %s", oe.Outcome.Message, req.Credential.ID)
	}
	marked := leaser.markedAuthExpired()
	if len(marked) != 1 || marked[0].credentialID != req.Credential.ID {
		t.Errorf("MarkAuthExpired calls = %+v, want exactly one for %s", marked, req.Credential.ID)
	}
}
