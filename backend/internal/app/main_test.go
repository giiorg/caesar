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

func TestMain_ContractCommandsWithoutTheirInputFileExitTwo(t *testing.T) {
	tests := []struct {
		name, command, inputPath string
	}{
		{name: "stage", command: "stage", inputPath: stageInputPath},
		{name: "extract", command: "extract", inputPath: extractInputPath},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := os.Stat(tc.inputPath); err == nil {
				t.Skipf("%s exists on this host, so the missing-input path cannot be exercised", tc.inputPath)
			}
			var out, errOut bytes.Buffer
			code := Main([]string{tc.command}, strings.NewReader(""), &out, &errOut)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr: %s)", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.inputPath) {
				t.Fatalf("stderr = %q, want it to name %q", errOut.String(), tc.inputPath)
			}
		})
	}
}

func TestMain_EgressProxyWithoutAUsableConfigExitsOne(t *testing.T) {
	tests := []struct {
		name, env, wantErr string
	}{
		{name: "no config at all", env: "", wantErr: "CAESAR_EGRESS_CONFIG"},
		{name: "config is not JSON", env: "not json", wantErr: "parse"},
		{name: "config names no attempt", env: `{"connect_allow":["registry.npmjs.org"]}`, wantErr: "attempt"},
		{
			name:    "an unauthenticated gateway may not hold a credential",
			env:     `{"attempt":"a","provider":{"endpoint":"https://api.anthropic.com","credential":"k","credential_header":"x-api-key"}}`,
			wantErr: "credential",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAESAR_EGRESS_CONFIG", tc.env)
			var out, errOut bytes.Buffer
			code := Main([]string{"egress-proxy"}, strings.NewReader(""), &out, &errOut)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (stderr: %s)", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want it to mention %q", errOut.String(), tc.wantErr)
			}
		})
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
