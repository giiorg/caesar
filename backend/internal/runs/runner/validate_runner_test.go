//go:build runnerintegration

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunValidation_HardenedExitCodeAndLog(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newDockerRunner(t, Options{}, scriptedHarness("", "true"), &recordingSink{})
	ref := dockerRef()

	res, err := r.RunValidation(t.Context(), ValidationSpec{
		Ref:     ref,
		Dir:     dir,
		Image:   gitImage,
		Cmd:     []string{"/bin/sh", "-c", "ls /workspace && echo validated && exit 7"},
		Timeout: 2 * time.Minute,
	})
	// A failing build is a result, not an infrastructure error: what it means
	// is Plan 08's decision, not this layer's.
	if err != nil {
		t.Fatalf("run validation: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", res.ExitCode)
	}
	if res.Truncated {
		t.Errorf("a three-line command reported a truncated log")
	}

	log := readLogFile(t, r, ref, validateLogFile)
	if !strings.Contains(log, "validated") || !strings.Contains(log, "keep.txt") {
		t.Errorf("validate.log = %q, want the command's output over the mounted dir", log)
	}
	if res.LogPath == "" {
		t.Error("result carries no log path")
	}
	if _, err := os.Stat(filepath.Join(r.opts.DataRoot, res.LogPath)); err != nil {
		t.Errorf("log path %q does not resolve under the data root: %v", res.LogPath, err)
	}

	// Validation owns its containers and its network — and nothing else. The
	// caller's directory is an input, never a thing to clean up.
	assertNoContainer(t, r, validateName(ref))
	assertNoContainer(t, r, validateGatewayName(ref))
	assertNoNetwork(t, r, validateNetworkName(ref))
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Errorf("validation deleted the caller's directory: %v", err)
	}
}

func TestRunValidation_NetworkIsGatewayOnly(t *testing.T) {
	// Two probes in one container: the internet head-on, with every proxy
	// variable cleared so the attempt is genuinely direct, and then a tunnel
	// to a host the allowlist does not name.
	script := `
command -v wget >/dev/null || exit 8
if HTTP_PROXY= HTTPS_PROXY= http_proxy= https_proxy= wget -T 3 -q -O /dev/null http://1.1.1.1/; then
  echo REACHED-THE-INTERNET
  exit 9
fi
echo no-direct-route
printf 'CONNECT denied.example:443 HTTP/1.1\r\nHost: denied.example:443\r\nConnection: close\r\n\r\n' | nc egress 8080
echo tunnel-attempted
exit 0
`
	r := newDockerRunner(t, Options{}, scriptedHarness("", "true"), &recordingSink{})
	ref := dockerRef()

	res, err := r.RunValidation(t.Context(), ValidationSpec{
		Ref:          ref,
		Dir:          t.TempDir(),
		Image:        gitImage,
		Cmd:          []string{"/bin/sh", "-c", script},
		ConnectAllow: []string{"registry.example"},
		Timeout:      2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("run validation: %v", err)
	}
	log := readLogFile(t, r, ref, validateLogFile)
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0\nvalidate.log:\n%s", res.ExitCode, log)
	}
	if !strings.Contains(log, "no-direct-route") {
		t.Errorf("the validation network had a route of its own: %q", log)
	}

	// The one way out is the gateway, and it is default-deny: an unlisted
	// host is refused there, in its own log.
	gwLog := readLogFile(t, r, ref, validateGatewayLogFile)
	if !strings.Contains(gwLog, "egress.denied") || !strings.Contains(gwLog, "denied.example") {
		t.Errorf("the validation gateway did not refuse the unlisted host:\n%s", gwLog)
	}
	if !strings.Contains(gwLog, "egress.summary") {
		t.Errorf("the validation gateway never flushed its summary:\n%s", gwLog)
	}
	assertNoContainer(t, r, validateName(ref))
	assertNoContainer(t, r, validateGatewayName(ref))
	assertNoNetwork(t, r, validateNetworkName(ref))
}
