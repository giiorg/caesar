package app

import (
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/config"
)

// The runner's whole on-disk tree exists after wiring, not after the first
// attempt of each kind happens to create one directory: an operator sizing or
// mounting logs/ and quarantine/ separately meets them on a fresh install.
func TestBuildRunner_CreatesTheDataRootTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	_, err := buildRunner(config.Runner{
		DataRoot: root, CaesarImage: "caesar:dev", GitImage: "caesar-git:dev",
	}, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("buildRunner: %v", err)
	}
	// Spelled out rather than ranged over runnerDataDirs: a test that read the
	// production list would keep passing if somebody emptied it.
	for _, dir := range []string{"attempts", "logs", "auth", "manifests", "quarantine"} {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil {
			t.Errorf("%s missing under the data root: %v", dir, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s has mode %04o, want 0700 — attempt workspaces and credentials live here", dir, perm)
		}
	}
}

func TestCloneURL_BuildsHostedURLsAndRejectsUnknownForge(t *testing.T) {
	tests := []struct {
		name    string
		forge   string
		slug    string
		want    string
		wantErr bool
	}{
		{name: "github", forge: "github", slug: "acme/widgets", want: "https://github.com/acme/widgets.git"},
		{name: "gitlab", forge: "gitlab", slug: "acme/widgets", want: "https://gitlab.com/acme/widgets.git"},
		{name: "unknown forge errors", forge: "bitbucket", slug: "acme/widgets", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloneURL(tt.forge, tt.slug)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("cloneURL(%q, %q) = %q, want error", tt.forge, tt.slug, got)
				}
				if !strings.Contains(err.Error(), tt.forge) {
					t.Errorf("error %v should name the offending forge %q", err, tt.forge)
				}
				return
			}
			if err != nil {
				t.Fatalf("cloneURL(%q, %q): %v", tt.forge, tt.slug, err)
			}
			if got != tt.want {
				t.Errorf("cloneURL(%q, %q) = %q, want %q", tt.forge, tt.slug, got, tt.want)
			}
		})
	}
}

func TestCloneAuthHeader_IsBasicXAccessToken(t *testing.T) {
	const token = "ghs_exampletoken123"
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	if got := cloneAuthHeader(token); got != want {
		t.Errorf("cloneAuthHeader(%q) = %q, want %q", token, got, want)
	}
}
