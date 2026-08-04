package app

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestMain_VersionPrintsBuildInfoAndExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"version"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "caesar ") {
		t.Fatalf("stdout = %q, want prefix %q", out.String(), "caesar ")
	}
}

func TestMain_UnknownCommandExitsTwoWithUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"frobnicate"}, strings.NewReader(""), &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("stderr = %q, want it to mention %q", errOut.String(), "unknown command")
	}
}

func TestMain_MigrateWithBadConfigExitsOne(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"migrate", "-config", "/nonexistent/caesar.json", "up"}, strings.NewReader(""), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %s)", code, errOut.String())
	}
}

func TestMain_StageWithoutItsContractFileExitsTwo(t *testing.T) {
	if _, err := os.Stat(stageInputPath); err == nil {
		t.Skipf("%s exists on this host, so the missing-input path cannot be exercised", stageInputPath)
	}
	var out, errOut bytes.Buffer
	code := Main([]string{"stage"}, strings.NewReader(""), &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), stageInputPath) {
		t.Fatalf("stderr = %q, want it to name %q", errOut.String(), stageInputPath)
	}
}

func TestMain_MigrateRejectsArgsAfterDirection(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"migrate", "up", "-config", "/nonexistent/caesar.json"}, strings.NewReader(""), &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "-config") {
		t.Fatalf("stderr = %q, want it to mention the misplaced flag", errOut.String())
	}
}
