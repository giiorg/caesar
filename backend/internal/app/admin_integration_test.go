//go:build integration

package app_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/app"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func writeConfig(t *testing.T, dbURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caesar.json")
	body := fmt.Sprintf(`{"database": {"url": %q}}`, dbURL)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAdminBootstrap_CreatesFirstAdminExactlyOnce(t *testing.T) {
	cfgPath := writeConfig(t, testdb.NewURL(t))

	var out, errOut bytes.Buffer
	code := app.Main(
		[]string{"admin", "bootstrap", "-config", cfgPath, "-email", "root@example.com"},
		strings.NewReader("a long enough password\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "root@example.com") {
		t.Fatalf("stdout = %q", out.String())
	}

	// Second run refuses.
	out.Reset()
	errOut.Reset()
	code = app.Main(
		[]string{"admin", "bootstrap", "-config", cfgPath, "-email", "other@example.com"},
		strings.NewReader("a long enough password\n"), &out, &errOut)
	if code != 1 {
		t.Fatalf("second bootstrap exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "admin already exists") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestAdminBootstrap_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		in   string
	}{
		{"missing email", []string{"admin", "bootstrap"}, "pw is long enough\n"},
		{"unknown subcommand", []string{"admin", "frobnicate"}, ""},
		{"empty password", []string{"admin", "bootstrap", "-email", "a@b.co"}, "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := app.Main(tt.args, strings.NewReader(tt.in), &out, &errOut); code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr: %s)", code, errOut.String())
			}
		})
	}
}
