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
	}, forgeEndpoints{}, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
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

// publicDefaults is what config ships, spelled out rather than read from
// config.defaults(): a test that read the production defaults would keep
// passing if somebody changed them.
var publicDefaults = forgeEndpoints{
	GitHub: "https://api.github.com", GitLab: "https://gitlab.com",
	GitHubClone: "https://github.com", GitLabClone: "https://gitlab.com",
}

func TestCloneURL_BuildsHostedURLsAndRejectsUnknownForge(t *testing.T) {
	tests := []struct {
		name    string
		eps     forgeEndpoints
		forge   string
		slug    string
		want    string
		wantErr bool
	}{
		{name: "github", eps: publicDefaults, forge: "github", slug: "acme/widgets", want: "https://github.com/acme/widgets.git"},
		{name: "gitlab", eps: publicDefaults, forge: "gitlab", slug: "acme/widgets", want: "https://gitlab.com/acme/widgets.git"},
		// A trailing slash on the configured base is an operator's honest
		// spelling, and joining it naively would produce a double slash.
		{
			name:  "a trailing slash on the base is absorbed",
			eps:   forgeEndpoints{GitHubClone: "https://github.com/"},
			forge: "github", slug: "acme/widgets", want: "https://github.com/acme/widgets.git",
		},
		{name: "unknown forge errors", eps: publicDefaults, forge: "bitbucket", slug: "acme/widgets", wantErr: true},
		// An unconfigured base must REFUSE, not emit "acme/widgets.git".
		// gitops.checkCloneURL rejects only unparseable URLs and userinfo, so a
		// relative path would sail through to `git clone` and fail far from
		// the cause.
		{name: "an unconfigured base errors", eps: forgeEndpoints{}, forge: "github", slug: "acme/widgets", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloneURL(tt.eps, tt.forge, tt.slug)
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

// The failure this guards is silent: API calls reach the self-hosted instance,
// clone and push still reach github.com, and the run reads as "can see the
// repo, publishes nothing".
func TestCloneURL_FollowsTheConfiguredForgeAndNotThePublicHost(t *testing.T) {
	got, err := cloneURL(forgeEndpoints{
		GitHub:      "https://git.example.internal/api/v3",
		GitHubClone: "https://git.example.internal",
	}, "github", "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://git.example.internal/acme/widgets.git"; got != want {
		t.Errorf("cloneURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "github.com") {
		t.Error("a self-hosted run must not clone from the public host")
	}
}

func TestCloneAuthHeader_IsBasicXAccessToken(t *testing.T) {
	const token = "ghs_exampletoken123"
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	if got := cloneAuthHeader(token); got != want {
		t.Errorf("cloneAuthHeader(%q) = %q, want %q", token, got, want)
	}
}
