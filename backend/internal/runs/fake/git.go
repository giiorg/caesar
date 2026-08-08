package fake

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The git the fakes run. Everything here talks to a LOCAL bare repository —
// the fake forge and the fake publisher are the only things in the spine's
// test doubles that touch a real remote, because a publication's whole
// correctness argument is about what is actually on one.

// gitEnv is the constructed environment every git command here runs on —
// never the inherited one (spec §3), so no user config, identity or clock
// leaks in and the fixtures hash identically on every machine.
func gitEnv(home string) []string {
	gitBin, _ := exec.LookPath("git")
	return []string{
		"PATH=" + filepath.Dir(gitBin),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=caesar", "GIT_AUTHOR_EMAIL=caesar@invalid",
		"GIT_COMMITTER_NAME=caesar", "GIT_COMMITTER_EMAIL=caesar@invalid",
	}
}

func git(dir string, env []string, args ...string) (string, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("fake: git binary: %w", err)
	}
	cmd := exec.Command(gitBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("fake: git %v: %w: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// bareBranchHead reports what a branch on a local bare repository points at.
// This is the fake forge's answer to BranchHead, and it reads the repository
// rather than a scripted map on purpose: the publisher pushes for real, so a
// scripted answer would drift from the truth the publication depends on.
func bareBranchHead(bare, branch string) (string, bool, error) {
	// for-each-ref rather than rev-parse or show-ref: it exits 0 whether the
	// ref exists or not and prints nothing when it does not, so "absent" is
	// an empty answer rather than an exit code this wrapper would have to
	// tell apart from a broken repository.
	out, err := git(bare, gitEnv(bare), "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
	if err != nil {
		return "", false, err
	}
	if out == "" {
		return "", false, nil
	}
	return out, true, nil
}

// AddFilePatch is the base64 wire form of a canonical "add this file" diff —
// the shape Extract produces and the publish path is handed. It is exported
// so a test can build the same patch the fake executor emits.
func AddFilePatch(name, content string) string {
	lines := strings.Count(content, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", name, name)
	b.WriteString("new file mode 100644\n")
	b.WriteString("--- /dev/null\n")
	fmt.Fprintf(&b, "+++ b/%s\n", name)
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", lines)
	for _, l := range strings.SplitAfter(content, "\n") {
		if l == "" {
			continue
		}
		b.WriteString("+" + l)
	}
	return base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// approvedTree is the git tree object id that applying AddFilePatch(name,
// content) to base produces — the value a real approval pins, and the value
// gitops.Publish compares its own `write-tree` against.
//
// It has to be a real tree id. The scripted executor used to hash the diff
// text and call the result a tree hash, which is publishable-looking and
// unpublishable in fact: Publish refuses it as a permanent publish_conflict
// before pushing anything, so every publication test would fail for a reason
// unrelated to what it exercises.
func approvedTree(bare, base, name, content string) (string, error) {
	home, err := os.MkdirTemp("", "caesar-fake-tree-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(home) }()

	repo := filepath.Join(home, "tree")
	env := gitEnv(home)
	if _, err := git(home, env, "clone", "-q", bare, repo); err != nil {
		return "", err
	}
	if _, err := git(repo, env, "checkout", "-q", "--detach", base); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		return "", err
	}
	if _, err := git(repo, env, "add", "--", name); err != nil {
		return "", err
	}
	return git(repo, env, "write-tree")
}
