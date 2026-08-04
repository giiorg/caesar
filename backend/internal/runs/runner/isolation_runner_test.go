//go:build runnerintegration

package runner

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// The isolation suite asks the agent container what its own world looks like.
// Every probe script writes one `key=value` line per finding to probe.out
// inside the workspace, and the test reads the file back out of the extracted
// patch: teardown removes the attempt directory, so the patch is both the only
// surviving copy and proof that collection itself worked.
const probeFile = "probe.out"

// probeReport reads one probe's findings out of the extracted patch. A probe
// that produced nothing is a broken probe, not a passing one, so an empty
// report fails loudly with the agent's own output attached.
func probeReport(t *testing.T, r *Runner, ref runs.AttemptRef, res runs.ExecutionResult) map[string]string {
	t.Helper()
	patch, ok := artifactsByType(res.Artifacts)[runs.ArtifactKindPatch]
	if !ok {
		t.Fatalf("no patch artifact in %v\nagent log:\n%s", res.Artifacts, readLogFile(t, r, ref, agentLogFile))
	}
	diff := decodedDiff(t, patch.Payload)

	report := map[string]string{}
	inProbe := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inProbe = strings.HasSuffix(line, " b/"+probeFile)
		case !inProbe, strings.HasPrefix(line, "+++"), !strings.HasPrefix(line, "+"):
			// Header lines and context: only the added body is the report.
		default:
			key, value, found := strings.Cut(strings.TrimSpace(line[1:]), "=")
			if !found {
				t.Errorf("probe wrote a line that is not key=value: %q", line)
				continue
			}
			report[key] = value
		}
	}
	if len(report) == 0 {
		t.Fatalf("the patch carries no %s:\n%s\nagent log:\n%s",
			probeFile, diff, readLogFile(t, r, ref, agentLogFile))
	}
	return report
}

func TestIsolation_ContainerHardeningApplied(t *testing.T) {
	// The container describes its own posture from the inside. That view is
	// the property that matters: Execute tears the container down before it
	// returns, so there is no corpse left for a host-side ContainerInspect to
	// describe, and an inspect would only report what was requested anyway —
	// not what the kernel actually applied.
	script := `set -u
OUT=/workspace/` + probeFile + `
: > "$OUT"
echo "uid=$(id -u)" >> "$OUT"
if touch /root-probe 2>/dev/null; then echo "rootfs=WRITABLE" >> "$OUT"; else echo "rootfs=READONLY" >> "$OUT"; fi
if touch /tmp/ok 2>/dev/null; then echo "tmpfs=WRITABLE" >> "$OUT"; else echo "tmpfs=READONLY" >> "$OUT"; fi
if touch /workspace/ok 2>/dev/null; then echo "workspace=WRITABLE" >> "$OUT"; else echo "workspace=READONLY" >> "$OUT"; fi
echo "capeff=$(awk '/^CapEff:/{print $2}' /proc/$$/status)" >> "$OUT"
echo "nonewprivs=$(awk '/^NoNewPrivs:/{print $2}' /proc/$$/status)" >> "$OUT"
exit 0
`
	r := newDockerRunner(t, Options{}, scriptedHarness("", script), &recordingSink{})
	ref := dockerRef()

	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	if err != nil {
		t.Fatalf("execute: %v\nagent log:\n%s", err, readLogFile(t, r, ref, agentLogFile))
	}
	report := probeReport(t, r, ref, res)

	// The uid is the control plane's own, so it is asserted as a property
	// rather than a value: whatever it is, it is never root.
	uid, err := strconv.Atoi(report["uid"])
	if err != nil || uid <= 0 {
		t.Errorf("the agent ran as uid %q, want a non-root uid", report["uid"])
	}
	delete(report, "uid")

	want := map[string]string{
		"rootfs":     "READONLY",
		"tmpfs":      "WRITABLE",
		"workspace":  "WRITABLE",
		"capeff":     "0000000000000000",
		"nonewprivs": "1",
	}
	if diff := cmp.Diff(want, report); diff != "" {
		t.Errorf("container hardening (-want +got):\n%s", diff)
	}
}

func TestIsolation_EscapeProbesAllDenied(t *testing.T) {
	// One line per target, every one expected to fail. wget carries a short
	// timeout so a blackholed route is answered in seconds rather than held
	// open until the attempt's deadline.
	//
	// The host gateway (probe 1) is the documented Docker caveat (spec §3): an
	// internal network still puts the bridge's own address in reach. Docker
	// Desktop and CI hosts run nothing on it, so the probe asserts a failed
	// connection; the VM firewall baseline is what makes that structural in
	// production. An internal network has no default route at all, so the
	// bridge address is derived from the subnet the container was given —
	// and if neither is discoverable the probe says NO-ROUTE rather than
	// quietly passing on a malformed URL.
	script := `set -u
OUT=/workspace/` + probeFile + `
: > "$OUT"

probe() {
  name=$1
  shift
  if "$@" >/dev/null 2>&1; then
    echo "$name=REACHED" >> "$OUT"
  else
    echo "$name=DENIED" >> "$OUT"
  fi
}

GW=$(ip route 2>/dev/null | awk '/default/{print $3; exit}')
if [ -z "$GW" ]; then
  GW=$(ip route 2>/dev/null | awk '/ src /{split($1, n, "/"); split(n[1], o, "."); print o[1]"."o[2]"."o[3]".1"; exit}')
fi
echo "probe: host gateway candidate is [$GW]"
if [ -z "$GW" ]; then
  echo "host-gateway=NO-ROUTE" >> "$OUT"
  echo "host-postgres=NO-ROUTE" >> "$OUT"
else
  probe host-gateway wget -T 3 -qO- "http://$GW:8080/"
  probe host-postgres wget -T 3 -qO- "http://$GW:5432/"
fi
probe metadata-service wget -T 3 -qO- http://169.254.169.254/latest/meta-data/
probe public-internet env -u HTTPS_PROXY -u HTTP_PROXY wget -T 3 -qO- https://example.com/
probe docker-socket test -S /var/run/docker.sock
probe caesar-api wget -T 3 -qO- http://host.docker.internal:8080/healthz
probe provider-direct wget -T 3 -qO- https://api.anthropic.com/v1/models
exit 0
`
	r := newDockerRunner(t, Options{}, scriptedHarness("", script), &recordingSink{})
	ref := dockerRef()

	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	if err != nil {
		t.Fatalf("execute: %v\nagent log:\n%s", err, readLogFile(t, r, ref, agentLogFile))
	}
	report := probeReport(t, r, ref, res)

	want := map[string]string{
		"host-gateway":     "DENIED",
		"host-postgres":    "DENIED",
		"metadata-service": "DENIED",
		"public-internet":  "DENIED",
		"docker-socket":    "DENIED",
		"caesar-api":       "DENIED",
		"provider-direct":  "DENIED",
	}
	if diff := cmp.Diff(want, report); diff != "" {
		t.Errorf("escape probes (-want +got):\n%s\nagent log:\n%s", diff, readLogFile(t, r, ref, agentLogFile))
	}

	// Stated as counts too, because that is the property in its own terms:
	// seven targets, seven refusals, nothing reached.
	var denied, reached int
	for _, verdict := range report {
		switch verdict {
		case "DENIED":
			denied++
		case "REACHED":
			reached++
		}
	}
	if denied != len(want) || reached != 0 {
		t.Errorf("%d denied / %d reached, want %d denied and none reached: %v", denied, reached, len(want), report)
	}
}

func TestIsolation_BrokerIsTheOnlyEgressPath(t *testing.T) {
	endpoint, seen := startFakeProvider(t)
	// busybox wget refuses a non-2xx answer with the status line on stderr,
	// which is how the two denials are told apart: an allowlisted call with
	// the attempt token succeeds, the same call without it is 401, and an
	// operation outside the harness's allowlist is 403.
	script := `set -u
OUT=/workspace/` + probeFile + `
: > "$OUT"

call() {
  name=$1
  shift
  if wget -T 3 -O "/tmp/body.$name" "$@" 2>"/tmp/err.$name"; then
    echo "$name=OK" >> "$OUT"
  else
    code=$(awk '/server returned error/{print $6; exit}' "/tmp/err.$name")
    if [ -z "$code" ]; then code=NO-RESPONSE; fi
    echo "$name=$code" >> "$OUT"
  fi
}

call allowlisted --header "x-api-key: $CAESAR_BROKER_TOKEN" "$CAESAR_BROKER_URL/v1/models"
call no-token "$CAESAR_BROKER_URL/v1/models"
call not-allowlisted --header "x-api-key: $CAESAR_BROKER_TOKEN" "$CAESAR_BROKER_URL/v1/admin"
echo "brokered-body=$(grep -c claude-fable-5 /tmp/body.allowlisted)" >> "$OUT"
exit 0
`
	r := newDockerRunner(t, Options{}, scriptedHarness(endpoint, script), &recordingSink{})
	ref := dockerRef()

	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	if err != nil {
		t.Fatalf("execute: %v\nagent log:\n%s", err, readLogFile(t, r, ref, agentLogFile))
	}

	want := map[string]string{
		"allowlisted":     "OK",
		"no-token":        "401",
		"not-allowlisted": "403",
		"brokered-body":   "1",
	}
	if diff := cmp.Diff(want, probeReport(t, r, ref, res)); diff != "" {
		t.Errorf("broker probes (-want +got):\n%s\ngateway log:\n%s", diff, readLogFile(t, r, ref, gatewayLogFile))
	}

	// Only the allowlisted call crossed the gateway: a refusal is answered by
	// the gateway itself and the provider never hears about it.
	if got := seen.snapshot(); got.requests != 1 {
		t.Errorf("the provider saw %d requests, want only the allowlisted one", got.requests)
	}

	// The gateway's own meter agrees, and the counters ride out on the
	// attempt's log_ref where the orchestrator reads them.
	egress, ok := artifactsByType(res.Artifacts)[runs.ArtifactKindLogRef].Payload["egress"].(map[string]any)
	if !ok {
		t.Fatalf("log_ref has no egress map: %v", res.Artifacts)
	}
	if egress["parse_error"] != nil {
		t.Fatalf("the attempt's egress bill was not harvested: %v\ngateway log:\n%s",
			egress, readLogFile(t, r, ref, gatewayLogFile))
	}
	if requests, _ := egress["requests"].(int64); requests < 1 {
		t.Errorf("egress.requests = %v, want at least the brokered call", egress["requests"])
	}
	if denied, _ := egress["denied"].(int64); denied < 2 {
		t.Errorf("egress.denied = %v, want at least the two refusals", egress["denied"])
	}
}

// The forge credential the credential-staging probe hunts for. It is staged
// exactly as production stages one — as an http.extraheader in the stage
// container's environment — and the fixture remote is a file:// URL, so git
// never consults it. That is deliberate: the property under test is that no
// forge credential is visible to the agent, not that this one authenticated
// anything.
var (
	forgeSecret     = "FORGE-SEKRET"
	forgeSecretB64  = base64.StdEncoding.EncodeToString([]byte("x:" + forgeSecret))
	forgeAuthHeader = "Authorization: Basic " + forgeSecretB64
)

// credentialedGit is fixtureGit with a forge credential attached, so the stage
// container runs the credentialed path the probe then fails to find any trace
// of.
type credentialedGit struct{ dir, header string }

func (g credentialedGit) CloneSpec(context.Context, runs.RepoRef) (CloneSpec, error) {
	return CloneSpec{URL: "file://" + remoteMount + "/bare.git", AuthHeader: g.header, BindDir: g.dir}, nil
}

// shellSplit renders s as two adjacent shell string literals. The probe greps
// for the secret, so the needle would otherwise appear verbatim in the script
// the shell was started with — and the script is exactly what the /proc
// cmdline scan reads back. Splitting it means the string exists in the
// container only after the shell concatenates it, and a cmdline hit is a real
// leak rather than the probe finding its own search pattern.
func shellSplit(s string) string {
	mid := len(s) / 2
	return `"` + s[:mid] + `""` + s[mid:] + `"`
}

func TestIsolation_AgentSeesNoForgeCredential(t *testing.T) {
	// Both spellings are hunted: the raw secret and the base64 blob the
	// header actually carries.
	//
	// The env and cmdline scans read a snapshot taken before any grep exists,
	// because a grep's own arguments carry the needle and would otherwise race
	// the scan that is reading /proc.
	script := fmt.Sprintf(`set -u
OUT=/workspace/%s
RAW=%s
B64=%s
: > "$OUT"

env > /tmp/env.txt
cat /proc/*/cmdline 2>/dev/null | tr '\0' '\n' > /tmp/cmdline.txt

scan() {
  if grep -q -- "$RAW" "$2" || grep -q -- "$B64" "$2"; then
    echo "$1=LEAKED" >> "$OUT"
  else
    echo "$1=CLEAN" >> "$OUT"
  fi
}
scan env /tmp/env.txt
scan cmdline /tmp/cmdline.txt

if grep -rq -- "$RAW" /workspace || grep -rq -- "$B64" /workspace; then
  echo "workspace=LEAKED" >> "$OUT"
else
  echo "workspace=CLEAN" >> "$OUT"
fi

echo "origin=$(git -C /workspace config --get remote.origin.url)" >> "$OUT"
git -C /workspace config --list --local > /tmp/gitconfig.txt 2>&1
if grep -Eiq 'extraheader|askpass|credential' /tmp/gitconfig.txt; then
  echo "gitconfig=AUTHKEY" >> "$OUT"
else
  echo "gitconfig=CLEAN" >> "$OUT"
fi

if [ -e /workspace/.git-credentials ]; then
  echo "git-credentials=PRESENT" >> "$OUT"
else
  echo "git-credentials=ABSENT" >> "$OUT"
fi
if [ -d /workspace/../base.git ]; then
  echo "base-mirror=PRESENT" >> "$OUT"
else
  echo "base-mirror=ABSENT" >> "$OUT"
fi
exit 0
`, probeFile, shellSplit(forgeSecret), shellSplit(forgeSecretB64))

	r := newDockerRunner(t, Options{}, scriptedHarness("", script), &recordingSink{})
	// The suite's constructor pins the credential-free fixture forge; this
	// test is the one that needs the credentialed variant.
	dir, _ := fixtureRepo(t)
	r.git = credentialedGit{dir: dir, header: forgeAuthHeader}
	ref := dockerRef()

	// A probe that finds nothing proves nothing unless there was something to
	// find: the attempt below really is staged with a forge credential.
	clone, err := r.git.CloneSpec(t.Context(), runs.RepoRef{})
	if err != nil || clone.AuthHeader != forgeAuthHeader {
		t.Fatalf("clone spec = %+v (err %v), want the staged forge credential", clone, err)
	}

	res, err := r.Execute(t.Context(), implementRequest(t, ref, fixtureSHA(t)))
	if err != nil {
		t.Fatalf("execute: %v\nagent log:\n%s", err, readLogFile(t, r, ref, agentLogFile))
	}

	want := map[string]string{
		"env":             "CLEAN",
		"cmdline":         "CLEAN",
		"workspace":       "CLEAN",
		"origin":          "file://" + remoteMount + "/bare.git",
		"gitconfig":       "CLEAN",
		"git-credentials": "ABSENT",
		// The trusted mirror extraction diffs against is one level up from the
		// workspace on the host and nowhere at all inside the container: only
		// the workspace is bound, so /workspace/.. is the container's own root.
		"base-mirror": "ABSENT",
	}
	if diff := cmp.Diff(want, probeReport(t, r, ref, res)); diff != "" {
		t.Errorf("credential-staging probes (-want +got):\n%s", diff)
	}

	// Nothing that outlives the attempt carries the secret either: not the
	// patch the probe rode out on, not the captured streams.
	patch := artifactsByType(res.Artifacts)[runs.ArtifactKindPatch].Payload
	for _, needle := range []string{forgeSecret, forgeSecretB64} {
		if strings.Contains(decodedDiff(t, patch), needle) {
			t.Errorf("the patch carries %q", needle)
		}
		for _, name := range []string{agentLogFile, gatewayLogFile} {
			if strings.Contains(readLogFile(t, r, ref, name), needle) {
				t.Errorf("%s carries %q", name, needle)
			}
		}
	}
}
