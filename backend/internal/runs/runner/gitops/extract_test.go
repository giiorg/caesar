package gitops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// stagedDir stages the fixture the way the pipeline does and hands back the
// attempt dir, so every extraction test starts from a real base.git plus a
// real (hostile-by-assumption) workspace.
func stagedDir(t *testing.T) (dir, baseSHA string) {
	t.Helper()
	remote, sha := stageFixture(t)
	dir = t.TempDir()
	if _, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 10000}, nil); err != nil {
		t.Fatal(err)
	}
	return dir, sha
}

func defaultCaps(baseSHA string) ExtractInput {
	return ExtractInput{BaseSHA: baseSHA, MaxFiles: 200000, MaxBytes: 2 << 30, MaxPatchBytes: 4 << 20}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// mkNestedRepo plants a genuine repository inside the workspace — the shape
// that turns into a gitlink the moment anything indexes the tree.
func mkNestedRepo(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "f.txt"), "inner\n")
	env := append(baseEnv(dir),
		"GIT_AUTHOR_NAME=caesar", "GIT_AUTHOR_EMAIL=caesar@example.com",
		"GIT_COMMITTER_NAME=caesar", "GIT_COMMITTER_EMAIL=caesar@example.com",
	)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"add", "."},
		{"commit", "-q", "-m", "inner"},
	} {
		if _, err := runGit(t.Context(), dir, env, args...); err != nil {
			t.Fatal(err)
		}
	}
}

// assertPatchApplies is the only claim that matters about a patch: a pristine
// checkout at base, plus the patch, is byte-for-byte the tree extraction
// reported. It deliberately reconstructs from the patch text alone.
func assertPatchApplies(t *testing.T, dir, baseSHA string, rep ExtractReport) {
	t.Helper()
	fresh := t.TempDir()
	env := baseEnv(fresh)
	checkout := filepath.Join(fresh, "checkout")
	if _, err := runGit(t.Context(), fresh, env, "clone", "-q", "--no-checkout", filepath.Join(dir, baseRepoDir), checkout); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(t.Context(), fresh, env, "-C", checkout, "checkout", "-q", "--detach", baseSHA); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(fresh, "patch.diff")
	if err := os.WriteFile(patch, rep.Diff, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(t.Context(), fresh, env, "-C", checkout, "apply", "--index", "--binary", patch); err != nil {
		t.Fatalf("the canonical patch does not apply to a pristine base: %v", err)
	}
	out, err := runGit(t.Context(), fresh, env, "-C", checkout, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out); got != rep.TreeHash {
		t.Fatalf("tree after applying the patch = %s, want the reported %s", got, rep.TreeHash)
	}
}

func TestExtract_NoChangesYieldsUnchanged(t *testing.T) {
	dir, sha := stagedDir(t)
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed || len(rep.Diff) != 0 {
		t.Fatalf("report = %+v, want an unchanged workspace with no patch", rep)
	}
	if rep.BaseSHA != sha || rep.TreeHash == "" {
		t.Fatalf("report = %+v, want base %s and a tree hash", rep, sha)
	}
}

func TestExtract_ModifiedAndNewFilesProduceApplyingPatch(t *testing.T) {
	dir, sha := stagedDir(t)
	ws := filepath.Join(dir, workspaceName)
	writeFile(t, filepath.Join(ws, "README.md"), "# changed\n")
	writeFile(t, filepath.Join(ws, "pkg", "new.txt"), "hello\n")
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed || rep.TreeHash == "" || len(rep.Diff) == 0 {
		t.Fatalf("report = %+v, want a changed tree with a patch", rep)
	}
	assertPatchApplies(t, dir, sha, rep)
}

func TestExtract_DeletionsAreInThePatch(t *testing.T) {
	dir, sha := stagedDir(t)
	if err := os.Remove(filepath.Join(dir, workspaceName, "README.md")); err != nil {
		t.Fatal(err)
	}
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed || !bytes.Contains(rep.Diff, []byte("deleted file mode")) {
		t.Fatalf("report = %+v, want the deletion in the patch", rep)
	}
	assertPatchApplies(t, dir, sha, rep)
}

func TestExtract_PoisonedWorkspaceGitConfigIsInert(t *testing.T) {
	dir, sha := stagedDir(t)
	ws := filepath.Join(dir, workspaceName)
	marker := filepath.Join(dir, "PWNED")
	poison := fmt.Sprintf("[diff]\n\texternal = /bin/sh -c 'touch %s'\n[core]\n\tfsmonitor = /bin/sh -c 'touch %s'\n\thooksPath = %s\n",
		marker, marker, filepath.Join(ws, ".git", "hooks"))
	appendFile(t, filepath.Join(ws, ".git", "config"), poison)
	for _, hook := range []string{"post-checkout", "pre-commit", "post-index-change"} {
		writeExecutable(t, filepath.Join(ws, ".git", "hooks", hook), "#!/bin/sh\ntouch "+marker+"\n")
	}
	writeFile(t, filepath.Join(ws, "README.md"), "# changed\n")

	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil || !rep.Changed {
		t.Fatalf("report = %+v err = %v", rep, err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("the hostile .git executed code during extraction")
	}
	if bytes.Contains(rep.Diff, []byte(".git/config")) || bytes.Contains(rep.Diff, []byte(".git/hooks")) {
		t.Fatalf("the workspace .git leaked into the patch:\n%s", rep.Diff)
	}
	assertPatchApplies(t, dir, sha, rep)
}

func TestExtract_RewrittenWorkspaceHistoryDoesNotMoveTheBase(t *testing.T) {
	dir, sha := stagedDir(t)
	ws := filepath.Join(dir, workspaceName)
	// The agent rewrites its own history and points HEAD anywhere it likes.
	// Extraction diffs against the trusted mirror, so none of it registers.
	writeFile(t, filepath.Join(ws, "README.md"), "# changed\n")
	env := append(baseEnv(dir),
		"GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@example.com",
		"GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@example.com",
	)
	for _, args := range [][]string{
		{"-C", ws, "add", "-A"},
		{"-C", ws, "commit", "-q", "-m", "agent commit"},
	} {
		if _, err := runGit(t.Context(), dir, env, args...); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	if rep.BaseSHA != sha || !rep.Changed {
		t.Fatalf("report = %+v, want the pinned base %s and a changed tree", rep, sha)
	}
	assertPatchApplies(t, dir, sha, rep)
}

func TestExtract_EmbeddedRepoRejectedTyped(t *testing.T) {
	dir, sha := stagedDir(t)
	mkNestedRepo(t, filepath.Join(dir, workspaceName, "vendor", "evil"))
	_, err := Extract(t.Context(), dir, defaultCaps(sha))
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeEmbeddedRepo || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v, want a permanent %s", err, runs.CodeEmbeddedRepo)
	}
	if !strings.Contains(te.Outcome.Message, "vendor/evil") {
		t.Fatalf("message = %q, want it to name the offending path", te.Outcome.Message)
	}
}

// A nested .git can be a *file* redirecting to a git directory the agent
// wrote from scratch. If anything resolved that redirect and honoured its
// config, extraction would be running agent-authored commands.
func TestExtract_NestedGitFileRedirectIsRejectedWithoutRunningIt(t *testing.T) {
	dir, sha := stagedDir(t)
	nested := filepath.Join(dir, workspaceName, "sub")
	mkNestedRepo(t, nested)

	marker := filepath.Join(dir, "PWNED")
	poisoned := filepath.Join(dir, "agent-gitdir")
	if err := os.Rename(filepath.Join(nested, ".git"), poisoned); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(dir, "agent-hooks")
	writeExecutable(t, filepath.Join(hooks, "post-index-change"), "#!/bin/sh\ntouch "+marker+"\n")
	appendFile(t, filepath.Join(poisoned, "config"), fmt.Sprintf(
		"[core]\n\tfsmonitor = /bin/sh -c 'touch %s'\n\thooksPath = %s\n[diff]\n\texternal = /bin/sh -c 'touch %s'\n",
		marker, hooks, marker))
	writeFile(t, filepath.Join(nested, ".git"), "gitdir: "+poisoned+"\n")

	_, err := Extract(t.Context(), dir, defaultCaps(sha))
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeEmbeddedRepo {
		t.Fatalf("err = %v, want %s", err, runs.CodeEmbeddedRepo)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("the redirected agent git directory executed code during extraction")
	}
}

func TestExtract_RenameIsDetectedInThePatch(t *testing.T) {
	dir, sha := stagedDir(t)
	ws := filepath.Join(dir, workspaceName)
	if err := os.MkdirAll(filepath.Join(ws, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(ws, "README.md"), filepath.Join(ws, "docs", "README.md")); err != nil {
		t.Fatal(err)
	}
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rename from README.md", "rename to docs/README.md"} {
		if !bytes.Contains(rep.Diff, []byte(want)) {
			t.Errorf("patch is missing %q:\n%s", want, rep.Diff)
		}
	}
	assertPatchApplies(t, dir, sha, rep)
}

func TestExtract_SymlinkModeAndBinaryContentSurvive(t *testing.T) {
	dir, sha := stagedDir(t)
	ws := filepath.Join(dir, workspaceName)
	if err := os.Symlink("README.md", filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(ws, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "blob.bin"), "\x00\x01\x02\xff")
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"120000", "100755", "GIT binary patch"} {
		if !bytes.Contains(rep.Diff, []byte(want)) {
			t.Errorf("patch is missing %q:\n%s", want, rep.Diff)
		}
	}
	assertPatchApplies(t, dir, sha, rep)
}

// A text file in a legacy encoding has no NUL early on, so git diffs it as
// text and its raw bytes land in the patch verbatim. The patch is only worth
// anything if those bytes survive the contract file byte for byte.
func TestExtract_NonUTF8PatchSurvivesTheContractRoundTrip(t *testing.T) {
	dir, sha := stagedDir(t)
	writeFile(t, filepath.Join(dir, workspaceName, "latin1.txt"), "caf\xe9 na\xefve\n")
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rep.Diff, []byte("+caf\xe9 na\xefve")) {
		t.Fatalf("the fixture did not put raw non-UTF-8 bytes in the patch:\n%q", rep.Diff)
	}

	blob, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back ExtractReport
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Diff, rep.Diff) {
		t.Fatalf("the patch did not survive the contract round trip:\n sent %q\n back %q", rep.Diff, back.Diff)
	}
	// And the round-tripped patch — the one Task 8 actually applies — still
	// reproduces the agent's tree exactly.
	assertPatchApplies(t, dir, sha, back)
}

func TestExtract_CapsAreTyped(t *testing.T) {
	tests := []struct {
		name  string
		tweak func(in *ExtractInput)
	}{
		{name: "the workspace file cap is a patch that is too large", tweak: func(in *ExtractInput) { in.MaxFiles = 1 }},
		{name: "the workspace byte cap is a patch that is too large", tweak: func(in *ExtractInput) { in.MaxBytes = 1 }},
		{name: "the diff cap is a patch that is too large", tweak: func(in *ExtractInput) { in.MaxPatchBytes = 10 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, sha := stagedDir(t)
			ws := filepath.Join(dir, workspaceName)
			writeFile(t, filepath.Join(ws, "a.txt"), strings.Repeat("a", 1024))
			writeFile(t, filepath.Join(ws, "b.txt"), strings.Repeat("b", 1024))
			in := defaultCaps(sha)
			tc.tweak(&in)
			_, err := Extract(t.Context(), dir, in)
			var te TypedError
			if !errors.As(err, &te) || te.Outcome.Code != runs.CodePatchTooLarge || te.Outcome.Class != runs.OutcomePermanent {
				t.Fatalf("err = %v, want a permanent %s", err, runs.CodePatchTooLarge)
			}
		})
	}
}

func TestExtract_NonPositiveCapsAreRefused(t *testing.T) {
	tests := []struct {
		name  string
		tweak func(in *ExtractInput)
	}{
		{name: "a missing file cap is not no cap", tweak: func(in *ExtractInput) { in.MaxFiles = 0 }},
		{name: "a missing byte cap is not no cap", tweak: func(in *ExtractInput) { in.MaxBytes = 0 }},
		{name: "a missing patch cap is not no cap", tweak: func(in *ExtractInput) { in.MaxPatchBytes = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, sha := stagedDir(t)
			in := defaultCaps(sha)
			tc.tweak(&in)
			_, err := Extract(t.Context(), dir, in)
			var te TypedError
			if !errors.As(err, &te) || te.Outcome.Code != runs.CodeExtractionFailed {
				t.Fatalf("err = %v, want %s", err, runs.CodeExtractionFailed)
			}
		})
	}
}

func TestParseObjectID_AcceptsOnlyAnObjectID(t *testing.T) {
	sha1, sha256 := strings.Repeat("a1", 20), strings.Repeat("b2", 32)
	tests := []struct {
		name, out, want string
		ok              bool
	}{
		{name: "a sha-1 id with git's trailing newline", out: sha1 + "\n", want: sha1, ok: true},
		{name: "a sha-256 id", out: sha256, want: sha256, ok: true},
		{name: "a warning printed ahead of the id", out: "warning: something\n" + sha1 + "\n"},
		{name: "uppercase hex is not what git prints", out: strings.ToUpper(sha1)},
		{name: "a truncated id", out: sha1[:39]},
		{name: "nothing at all", out: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseObjectID(tc.out)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("parseObjectID(%q) = %q, %v; want %q, %v", tc.out, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The filesystem contract is the control plane's, not the agent's work: it is
// staged into the workspace and must not show up as a change the phase made.
func TestExtract_CaesarInputsNeverReachThePatch(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := stageInto(t, remote, sha, func(in *StageInput) {
		in.Instructions = "implement the thing\n"
		in.TaskInputs = map[string]any{"ticket": "CAESAR-12"}
		in.Contexts = []StageContext{{Name: "0-plan.json", JSON: json.RawMessage(`{"content":"the plan"}`)}}
	})
	ws := filepath.Join(dir, workspaceName)

	// A staged contract plus whatever the agent wrote under it is still no
	// change at all.
	writeFile(t, filepath.Join(ws, caesarDir, "in", "agent-scratch.txt"), "scratch\n")
	writeFile(t, filepath.Join(ws, caesarDir, "out", "artifacts.json"), `[{"type":"plan","payload":{}}]`)
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed || len(rep.Diff) != 0 {
		t.Fatalf("changed = %v, want an unchanged tree — %s is not the phase's work:\n%s",
			rep.Changed, caesarDir, rep.Diff)
	}

	// Alongside a real change, the patch carries the real change and nothing
	// else.
	writeFile(t, filepath.Join(ws, "real.txt"), "the agent's work\n")
	rep, err = Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed || !bytes.Contains(rep.Diff, []byte("real.txt")) {
		t.Fatalf("the real change is missing from the patch:\n%s", rep.Diff)
	}
	if bytes.Contains(rep.Diff, []byte(caesarDir)) {
		t.Fatalf("%s leaked into the patch:\n%s", caesarDir, rep.Diff)
	}
	assertPatchApplies(t, dir, sha, rep)
}

// The second of the two mechanisms, proved on its own: with the mirror's
// exclude line gone, Extract's explicit pathspec still keeps the contract
// directory out of both the index and the diff.
func TestExtract_PathspecHidesCaesarWithoutTheMirrorExclude(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := stageInto(t, remote, sha, func(in *StageInput) { in.Instructions = "implement the thing\n" })

	exclude := filepath.Join(dir, baseRepoDir, "info", "exclude")
	raw, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.ReplaceAll(string(raw), "/"+caesarDir+"/", "")
	if stripped == string(raw) {
		t.Fatalf("the mirror exclude never carried the %s line: %q", caesarDir, raw)
	}
	if err := os.WriteFile(exclude, []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, workspaceName, "real.txt"), "the agent's work\n")
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed || !bytes.Contains(rep.Diff, []byte("real.txt")) {
		t.Fatalf("the real change is missing from the patch:\n%s", rep.Diff)
	}
	if bytes.Contains(rep.Diff, []byte(caesarDir)) {
		t.Fatalf("%s leaked into the patch with only the pathspec guarding it:\n%s", caesarDir, rep.Diff)
	}
	assertPatchApplies(t, dir, sha, rep)
}

func TestExtract_MissingBaseSHAIsExtractionFailed(t *testing.T) {
	dir, _ := stagedDir(t)
	_, err := Extract(t.Context(), dir, defaultCaps(strings.Repeat("a", 40)))
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeExtractionFailed || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v, want a permanent %s", err, runs.CodeExtractionFailed)
	}
}

func TestExtract_MissingWorkspaceIsExtractionFailed(t *testing.T) {
	dir, sha := stagedDir(t)
	if err := os.RemoveAll(filepath.Join(dir, workspaceName)); err != nil {
		t.Fatal(err)
	}
	_, err := Extract(t.Context(), dir, defaultCaps(sha))
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeExtractionFailed {
		t.Fatalf("err = %v, want %s", err, runs.CodeExtractionFailed)
	}
}
