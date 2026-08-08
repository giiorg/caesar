package gitops

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// initRemote is the brief's name for the fixture stage_test.go already has: a
// bare repo with one deterministic seed commit, and its head.
func initRemote(t *testing.T) (remote, base string) {
	t.Helper()
	return stageFixture(t)
}

// gitOut runs a read-only git command in dir on a constructed env and hands
// back its TRIMMED output — every assertion in this file compares against a
// bare literal, and git's formats all end in a newline.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(t.Context(), dir, baseEnv(t.TempDir()), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

// addFilePatch is the base64 wire form of a canonical "add this file" diff —
// the shape Extract produces and the publish path is handed.
func addFilePatch(name, content string) string {
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

// approvedTree is the tree hash applying patch to base produces — the value an
// approval would have pinned, and now a required input. Built with plain git in
// a throwaway clone rather than through publishPrepare, so the expectation is
// independent of the code under test.
func approvedTree(t *testing.T, remote, base, patch string) string {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "tree")
	env := baseEnv(home)
	mustGit(t, home, env, "clone", "-q", remote, repo)
	mustGit(t, repo, env, "checkout", "-q", "--detach", base)
	raw, err := base64.StdEncoding.DecodeString(patch)
	if err != nil {
		t.Fatalf("approvedTree needs a decodable patch: %v", err)
	}
	if len(raw) > 0 {
		p := filepath.Join(home, "approved.patch")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		mustGit(t, repo, env, "apply", "--index", "--whitespace=nowarn", p)
	}
	out, err := runGit(t.Context(), repo, env, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	return strings.TrimSpace(out)
}

// unreachedTree stands in for the approved tree hash in the cases that refuse
// before any tree is built. It is never compared against anything; it exists
// only because an empty TreeHash is itself a refusal.
const unreachedTree = "1111111111111111111111111111111111111111"

// pinGitLocalZone forces a non-UTC local timezone onto every git command
// Publish runs. Without it "the commit stamp carries its own zone" is only
// falsifiable on a host that is not already UTC: a zone-less date layout would
// be interpreted in local time and still render as +00:00 under TZ=UTC, so the
// assertion would pass on exactly the machines CI runs on.
//
// tz must be a POSIX TZ string ("JST-9"), never a zone NAME ("Asia/Tokyo"):
// a name that no zoneinfo database can resolve makes git fall back to UTC
// silently, which would undo the pin without failing anything.
func pinGitLocalZone(t *testing.T, tz string) {
	t.Helper()
	orig := runGitFn
	runGitFn = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		return orig(ctx, dir, append(slices.Clone(env), "TZ="+tz), args...)
	}
	t.Cleanup(func() { runGitFn = orig })
}

// plantAuthConfigAfterClone persists an auth key into the publish checkout the
// moment it is cloned. git never does this on its own with GIT_CONFIG_*-scoped
// values, so this seam is the only way to produce the condition
// assertNoCredentialConfig exists for.
func plantAuthConfigAfterClone(t *testing.T, value string) {
	t.Helper()
	orig := runGitFn
	runGitFn = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		out, err := orig(ctx, dir, env, args...)
		if err == nil && len(args) > 0 && args[0] == "clone" {
			mustGit(t, dir, baseEnv(dir), "-C", args[len(args)-1],
				"config", "--local", "http.extraheader", value)
		}
		return out, err
	}
	t.Cleanup(func() { runGitFn = orig })
}

// pushForeignCommit puts somebody else's commit on branch and returns its sha.
func pushForeignCommit(t *testing.T, remote, branch, parent string) string {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "foreign")
	env := baseEnv(home)
	mustGit(t, home, env, "clone", "-q", remote, repo)
	mustGit(t, repo, env, "checkout", "-q", "--detach", parent)
	if err := os.WriteFile(filepath.Join(repo, "FOREIGN.md"), []byte("not ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, env, "add", "-A")
	mustGit(t, repo, env, "commit", "-q", "-m", "foreign")
	mustGit(t, repo, env, "push", "-q", "--force", "origin", "HEAD:refs/heads/"+branch)
	out, err := runGit(t.Context(), repo, env, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// createBranchAfterPrepare swaps runGitFn so the branch appears on the remote
// in the window between the ls-remote probe and the push — the create path's
// race, staged deterministically. It hooks the push specifically: any earlier
// command would be a different race than the one under test.
func createBranchAfterPrepare(t *testing.T, remote, branch, base string) {
	t.Helper()
	orig := runGitFn
	var once sync.Once
	runGitFn = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "push" {
			once.Do(func() {
				home := t.TempDir()
				mustGit(t, home, baseEnv(home), "-C", remote, "update-ref", "refs/heads/"+branch, base)
			})
		}
		return orig(ctx, dir, env, args...)
	}
	t.Cleanup(func() { runGitFn = orig })
}

// gitRecorder answers one question per git subcommand: did it see the
// credential? A local bare repo authenticates nothing, so this is the only
// way the wiring is observable at all.
type gitRecorder struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (r *gitRecorder) ranWithAuth(cmd string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[cmd]
}

func recordGitInvocations(t *testing.T) *gitRecorder {
	t.Helper()
	rec := &gitRecorder{seen: map[string]bool{}}
	orig := runGitFn
	runGitFn = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		if len(args) > 0 {
			hasAuth := slices.ContainsFunc(env, func(kv string) bool {
				return strings.HasPrefix(kv, "GIT_CONFIG_VALUE_0=")
			})
			rec.mu.Lock()
			rec.seen[args[0]] = rec.seen[args[0]] || hasAuth
			rec.mu.Unlock()
		}
		return orig(ctx, dir, env, args...)
	}
	t.Cleanup(func() { runGitFn = orig })
	return rec
}

func mustGit(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	if _, err := runGit(t.Context(), dir, env, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}
