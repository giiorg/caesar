package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestMain_VersionPrintsBuildInfoAndExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "caesar ") {
		t.Fatalf("stdout = %q, want prefix %q", out.String(), "caesar ")
	}
}

func TestMain_UnknownCommandExitsTwoWithUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"frobnicate"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("stderr = %q, want it to mention %q", errOut.String(), "unknown command")
	}
}

func TestMain_MigrateWithBadConfigExitsOne(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"migrate", "-config", "/nonexistent/caesar.json", "up"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %s)", code, errOut.String())
	}
}
