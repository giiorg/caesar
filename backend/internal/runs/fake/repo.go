package fake

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InitBareRepo creates a bare repository with one deterministic commit
// and returns its head SHA. Requires the git binary (dev + CI have it);
// callers are tests and the golden-set seed only.
func InitBareRepo(dir string) (string, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("fake repo: git binary: %w", err)
	}
	work := filepath.Join(dir, "work")
	bare := filepath.Join(dir, "bare.git")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# golden set seed\n"), 0o600); err != nil {
		return "", err
	}
	// A constructed environment, never the inherited one (spec §3): git
	// sees only what is listed here, so no user or system config, identity,
	// or clock leaks in and the head SHA is the same on every machine.
	env := []string{
		"PATH=" + filepath.Dir(gitBin),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=caesar", "GIT_AUTHOR_EMAIL=caesar@example.com",
		"GIT_COMMITTER_NAME=caesar", "GIT_COMMITTER_EMAIL=caesar@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
	steps := [][]string{
		{"-C", work, "init", "-q", "-b", "main"},
		{"-C", work, "add", "."},
		{"-C", work, "commit", "-q", "-m", "seed"},
		{"clone", "-q", "--bare", work, bare},
		{"-C", bare, "rev-parse", "HEAD"},
	}
	var last []byte
	for _, args := range steps {
		cmd := exec.Command(gitBin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("fake repo: git %v: %s", args, out)
		}
		last = out
	}
	return strings.TrimSpace(string(last)), nil
}
