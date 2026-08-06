package gitops

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

func stageFixture(t *testing.T) (remoteDir, headSHA string) {
	t.Helper()
	dir := t.TempDir()
	sha, err := fake.InitBareRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "bare.git"), sha
}

// stageInto stages a fresh attempt dir from an existing fixture remote, so a
// multi-phase test runs every phase against one repository — which is what a
// run does.
func stageInto(t *testing.T, remote, sha string, lineage func(*StageInput)) string {
	t.Helper()
	dir := t.TempDir()
	in := StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 10000}
	if lineage != nil {
		lineage(&in)
	}
	if _, err := Stage(t.Context(), dir, in, nil); err != nil {
		t.Fatalf("stage: %v", err)
	}
	return dir
}

// stageForLineage is the failure half of stageInto: it hands back the error
// rather than failing the test, for the cases that are about refusals.
func stageForLineage(t *testing.T, remote, sha string, lineage func(*StageInput)) error {
	t.Helper()
	in := StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 10000}
	lineage(&in)
	_, err := Stage(t.Context(), t.TempDir(), in, nil)
	return err
}

func readWorkspaceFile(t *testing.T, dir, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, workspaceName, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// extractPatch runs extraction the way the pipeline does and insists there is
// a patch to talk about.
func extractPatch(t *testing.T, dir, baseSHA string) ExtractReport {
	t.Helper()
	rep, err := Extract(t.Context(), dir, defaultCaps(baseSHA))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !rep.Changed || len(rep.Diff) == 0 {
		t.Fatalf("extract produced no patch: %+v", rep)
	}
	return rep
}

// The cumulative-patch contract, end to end through the two halves that
// implement it: extraction always diffs from the run's base, and staging
// applies exactly one patch — so the newest patch alone carries the whole
// linear chain, and a third phase given only it opens where the second left
// off. Chaining patches instead would double-apply every ancestor.
func TestStage_CumulativePatchSubsumesItsAncestor(t *testing.T) {
	remote, sha := stageFixture(t)

	// Phase 1 writes a file.
	d1 := stageInto(t, remote, sha, nil)
	writeFile(t, filepath.Join(d1, workspaceName, "a.txt"), "one\n")
	first := extractPatch(t, d1, sha)

	// Phase 2 is handed phase 1's patch: its workspace opens where phase 1
	// left off, and it edits the very line phase 1 wrote.
	d2 := stageInto(t, remote, sha, func(in *StageInput) {
		in.Patch = base64.StdEncoding.EncodeToString(first.Diff)
	})
	if got := readWorkspaceFile(t, d2, "a.txt"); got != "one\n" {
		t.Fatalf("phase 2 workspace a.txt = %q, want phase 1's content", got)
	}
	writeFile(t, filepath.Join(d2, workspaceName, "a.txt"), "one\ntwo\n")
	second := extractPatch(t, d2, sha)

	// The patch diffs from the run base, so it carries both phases' work.
	for _, want := range []string{"+one", "+two"} {
		if !bytes.Contains(second.Diff, []byte(want)) {
			t.Fatalf("phase 2's patch is missing %q — it is not cumulative:\n%s", want, second.Diff)
		}
	}

	// And the newest patch ALONE reproduces the composition.
	d3 := stageInto(t, remote, sha, func(in *StageInput) {
		in.Patch = base64.StdEncoding.EncodeToString(second.Diff)
	})
	if got := readWorkspaceFile(t, d3, "a.txt"); got != "one\ntwo\n" {
		t.Fatalf("phase 3 workspace a.txt = %q, want the whole chain", got)
	}
}

func TestStage_PatchThatDoesNotApplyIsPermanentStagingFailed(t *testing.T) {
	remote, sha := stageFixture(t)
	// A patch whose pre-image is not what the pinned base holds — the shape of
	// a lineage whose base moved underneath it.
	stale := `diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-# a line the base does not carry
+# rewritten
`
	err := stageForLineage(t, remote, sha, func(in *StageInput) {
		in.Patch = base64.StdEncoding.EncodeToString([]byte(stale))
	})
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingFailed || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v, want a permanent %s", err, runs.CodeStagingFailed)
	}
	// git's own text names hunks and paths out of a patch we did not write;
	// the caller gets the fact, not that noise.
	if strings.Contains(te.Outcome.Message, "gitops:") || strings.Contains(te.Outcome.Message, "@@") {
		t.Fatalf("message leaked git's raw error: %q", te.Outcome.Message)
	}
}

func TestStage_MalformedPatchEncodingIsPermanentStagingFailed(t *testing.T) {
	remote, sha := stageFixture(t)
	err := stageForLineage(t, remote, sha, func(in *StageInput) { in.Patch = "not base64!!" })
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingFailed || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v, want a permanent %s", err, runs.CodeStagingFailed)
	}
}

func TestStage_LineageCapsAreTypedAndNameTheBound(t *testing.T) {
	tests := []struct {
		name    string
		cap     int
		lineage func(*StageInput)
	}{
		{
			name: "a patch one byte past the decoded cap",
			cap:  maxInputPatchBytes,
			lineage: func(in *StageInput) {
				in.Patch = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("p"), maxInputPatchBytes+1))
			},
		},
		{
			name:    "instructions one byte past the cap",
			cap:     maxInstructionsBytes,
			lineage: func(in *StageInput) { in.Instructions = strings.Repeat("i", maxInstructionsBytes+1) },
		},
		{
			name: "a context artifact one byte past the cap",
			cap:  maxContextBytes,
			lineage: func(in *StageInput) {
				in.Contexts = []StageContext{{
					Name: "0-plan.json",
					JSON: json.RawMessage(bytes.Repeat([]byte("c"), maxContextBytes+1)),
				}}
			},
		},
	}
	remote, sha := stageFixture(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := stageForLineage(t, remote, sha, tc.lineage)
			var te TypedError
			if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingFailed || te.Outcome.Class != runs.OutcomePermanent {
				t.Fatalf("err = %v, want a permanent %s", err, runs.CodeStagingFailed)
			}
			if !strings.Contains(te.Outcome.Message, strconv.Itoa(tc.cap)) {
				t.Fatalf("message = %q, want it to name the %d byte bound", te.Outcome.Message, tc.cap)
			}
		})
	}
}

// A context filename is a name, never a path: the contract file is written by
// the control plane, and a traversing name would put an input file outside the
// workspace the phase is confined to.
func TestStage_ContextNameMustBeAPlainFilename(t *testing.T) {
	remote, sha := stageFixture(t)
	for _, name := range []string{"../escaped.json", "sub/0-plan.json", "", ".."} {
		t.Run(name, func(t *testing.T) {
			err := stageForLineage(t, remote, sha, func(in *StageInput) {
				in.Contexts = []StageContext{{Name: name, JSON: json.RawMessage(`{}`)}}
			})
			var te TypedError
			if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingFailed || te.Outcome.Class != runs.OutcomePermanent {
				t.Fatalf("err = %v, want a permanent %s", err, runs.CodeStagingFailed)
			}
		})
	}
}

func TestStage_WritesTheCaesarInputContract(t *testing.T) {
	remote, sha := stageFixture(t)
	patchID, planID := "1a2b", "3c4d"
	dir := stageInto(t, remote, sha, func(in *StageInput) {
		in.Instructions = "implement the thing\n"
		in.TaskInputs = map[string]any{"ticket": "CAESAR-12"}
		in.Contexts = []StageContext{{Name: "1-plan.json", JSON: json.RawMessage(`{"content":"the plan"}`)}}
		in.InputRefs = []InputRef{
			{ID: patchID, Type: "patch"},
			{ID: planID, Type: "plan", Name: "1-plan.json"},
		}
	})

	if got := readWorkspaceFile(t, dir, ".caesar/in/instructions.md"); got != "implement the thing\n" {
		t.Errorf("instructions.md = %q", got)
	}
	if got := readWorkspaceFile(t, dir, ".caesar/in/1-plan.json"); got != `{"content":"the plan"}` {
		t.Errorf("context file = %q", got)
	}

	var manifest struct {
		TaskInputs map[string]any `json:"task_inputs"`
		Inputs     []InputRef     `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(readWorkspaceFile(t, dir, ".caesar/in/inputs.json")), &manifest); err != nil {
		t.Fatalf("inputs.json: %v", err)
	}
	if manifest.TaskInputs["ticket"] != "CAESAR-12" {
		t.Errorf("task_inputs = %v", manifest.TaskInputs)
	}
	want := []InputRef{{ID: patchID, Type: "patch"}, {ID: planID, Type: "plan", Name: "1-plan.json"}}
	if len(manifest.Inputs) != len(want) {
		t.Fatalf("inputs = %+v, want %+v", manifest.Inputs, want)
	}
	for i, ref := range want {
		if manifest.Inputs[i] != ref {
			t.Errorf("inputs[%d] = %+v, want %+v", i, manifest.Inputs[i], ref)
		}
	}

	// out/ is the harness's side of the contract: it exists, and it is the
	// harness that puts anything in it.
	entries, err := os.ReadDir(filepath.Join(dir, workspaceName, caesarDir, "out"))
	if err != nil {
		t.Fatalf("read .caesar/out: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf(".caesar/out = %v, want it created empty", entries)
	}
}

// A harness entry script reads instructions.md unconditionally, so the
// contract exists even for a phase that was handed no lineage at all.
func TestStage_CaesarContractExistsWithoutLineage(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := stageInto(t, remote, sha, nil)
	if got := readWorkspaceFile(t, dir, ".caesar/in/instructions.md"); got != "" {
		t.Errorf("instructions.md = %q, want empty", got)
	}
	var manifest struct {
		TaskInputs map[string]any `json:"task_inputs"`
		Inputs     []InputRef     `json:"inputs"`
	}
	raw := readWorkspaceFile(t, dir, ".caesar/in/inputs.json")
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		t.Fatalf("inputs.json: %v", err)
	}
	// Empty, never null: the harness reads an object and an array either way.
	if manifest.TaskInputs == nil || manifest.Inputs == nil {
		t.Errorf("inputs.json = %s, want empty containers rather than nulls", raw)
	}
}

// The first of the two mechanisms that keep .caesar out of the patch, proved
// on its own: the exclude line staging writes into the TRUSTED mirror governs
// the index extraction builds. This is the authoritative one — base.git is
// outside the agent's bind, so nothing the agent does can re-include itself.
func TestStage_MirrorExcludeHidesCaesarFromTheTrustedIndex(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := stageInto(t, remote, sha, func(in *StageInput) { in.Instructions = "hello\n" })
	ws := filepath.Join(dir, workspaceName)

	// Extraction's indexing step, deliberately WITHOUT the belt-and-braces
	// pathspec: what is left is the exclude file alone.
	env := append(baseEnv(dir),
		"GIT_DIR="+filepath.Join(dir, baseRepoDir),
		"GIT_WORK_TREE="+ws,
		"GIT_INDEX_FILE="+filepath.Join(dir, "probe.index"),
	)
	for _, args := range [][]string{{"read-tree", sha}, {"add", "-A"}} {
		if _, err := runGit(t.Context(), dir, env, args...); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runGit(t.Context(), dir, env, "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, caesarDir) {
		t.Fatalf("the mirror's exclude did not hide %s on its own:\n%s", caesarDir, out)
	}

	// The agent's own .git carries the line too — status hygiene, no security
	// weight, since the agent owns that file.
	raw, err := os.ReadFile(filepath.Join(ws, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/"+caesarDir+"/") {
		t.Errorf("workspace exclude = %q, want the %s line", raw, caesarDir)
	}
}

// grepTree walks every regular file under root and reports the ones
// containing needle — the belt-and-braces check that a credential the
// verify scan is meant to catch is genuinely absent from disk.
func grepTree(t *testing.T, root, needle string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(needle)) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestStage_HappyPath_ClonesSanitizesVerifies(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	rep, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Verified || rep.HeadSHA != sha || rep.Files < 1 {
		t.Fatalf("report = %+v", rep)
	}
	// Workspace is detached at the pinned SHA.
	out, err := runGit(t.Context(), dir, baseEnv(dir), "-C", filepath.Join(dir, "workspace"), "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(out) != sha {
		t.Fatalf("workspace head = %q err=%v", out, err)
	}
	// A real local .git the harness can use, and the mirror stays put.
	for _, want := range []string{"base.git", "workspace/.git"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Fatalf("missing %s: %v", want, err)
		}
	}
}

func TestStage_OriginPointsAtTheCloneURL(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	if _, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	out, err := runGit(t.Context(), dir, baseEnv(dir), "-C", filepath.Join(dir, "workspace"), "remote", "get-url", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != remote {
		t.Fatalf("origin = %q, want %q", strings.TrimSpace(out), remote)
	}
}

func TestStage_TokenInURLRefusedBeforeAnyGitRuns(t *testing.T) {
	dir := t.TempDir()
	_, err := Stage(t.Context(), dir, StageInput{CloneURL: "https://x-access-token:sekret@github.com/a/b.git", BaseSHA: "abc", QuotaBytes: 1, QuotaFiles: 1}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "base.git")); !os.IsNotExist(statErr) {
		t.Fatal("no clone may happen with a token-bearing URL")
	}
}

func TestStage_UnparseableURLIsRefused(t *testing.T) {
	dir := t.TempDir()
	_, err := Stage(t.Context(), dir, StageInput{CloneURL: "git@github.com:a/b.git", BaseSHA: "abc", QuotaBytes: 1, QuotaFiles: 1}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("err = %v", err)
	}
}

func TestStage_MissingBaseSHAIsTyped(t *testing.T) {
	remote, _ := stageFixture(t)
	_, err := Stage(t.Context(), t.TempDir(), StageInput{CloneURL: remote, BaseSHA: strings.Repeat("a", 40), QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeBaseSHAUnavailable || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v", err)
	}
}

func TestStage_CloneFailureIsTransientStagingFailed(t *testing.T) {
	_, err := Stage(t.Context(), t.TempDir(), StageInput{CloneURL: filepath.Join(t.TempDir(), "nope.git"), BaseSHA: "abc", QuotaBytes: 1, QuotaFiles: 1}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingFailed || te.Outcome.Class != runs.OutcomeTransient {
		t.Fatalf("err = %v", err)
	}
}

func TestVerify_PlantedCredentialIsCaught(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	secret := "Authorization: Basic c2VrcmV0"
	// Stage clean first, then plant the secret and re-verify directly.
	if _, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.git", "FETCH_HEAD"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyStaging(dir, secret, 1<<30, 1000)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("planted credential survived verification: %v", err)
	}
}

func TestVerify_PlantedCredentialInWorkspaceGitIsCaught(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	secret := "Authorization: Basic c2VrcmV0"
	if _, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace", ".git", "FETCH_HEAD"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyStaging(dir, secret, 1<<30, 1000)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("planted credential survived verification: %v", err)
	}
}

func TestVerify_PlantedGitCredentialsFileIsCaught(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	if _, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	// $HOME is the attempt dir, so this is exactly where a credential
	// helper would have parked one.
	if err := os.WriteFile(filepath.Join(dir, ".git-credentials"), []byte("https://u:p@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyStaging(dir, "", 1<<30, 1000)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("planted .git-credentials survived verification: %v", err)
	}
}

func TestVerify_CredentialInObjectsIsNotScanned(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	secret := "Authorization: Basic c2VrcmV0"
	if _, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	// Packfiles hold repo content, not clone metadata: a byte sequence in
	// there is the repository's own, so scanning it would be a false positive.
	if err := os.WriteFile(filepath.Join(dir, "base.git", "objects", "planted"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyStaging(dir, secret, 1<<30, 1000); err != nil {
		t.Fatalf("objects/ must be excluded from the credential scan: %v", err)
	}
}

func TestSanitize_ExtraheaderInRepoConfigIsCaught(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	if _, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "workspace")
	if _, err := runGit(t.Context(), dir, baseEnv(dir), "-C", ws, "config", "--local", "http.extraheader", "Authorization: Basic c2VrcmV0"); err != nil {
		t.Fatal(err)
	}
	err := assertNoCredentialConfig(t.Context(), dir, filepath.Join(dir, "base.git"), ws)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("persisted auth config survived the sanitize scan: %v", err)
	}
	if strings.Contains(te.Outcome.Message, "c2VrcmV0") {
		t.Fatalf("the message leaked the credential value: %q", te.Outcome.Message)
	}
}

func TestStage_CloneExceedingQuotaIsTyped(t *testing.T) {
	remote, sha := stageFixture(t)
	_, err := Stage(t.Context(), t.TempDir(), StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1, QuotaFiles: 1000}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeWorkspaceQuotaExceeded {
		t.Fatalf("err = %v", err)
	}
}

func TestStage_AuthHeaderNeverPersistsToDisk(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	authEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic ZmFrZXRva2Vu",
	}
	rep, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, authEnv)
	if err != nil || !rep.Verified {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	// The verify scan ran with the real header value — belt and braces here:
	found := grepTree(t, dir, "ZmFrZXRva2Vu")
	if len(found) != 0 {
		t.Fatalf("credential persisted in: %v", found)
	}
}

func TestBoundedWalk_StopsAtTheCapAndSkipsTopLevelGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "big"), bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name                       string
		skipTopGit                 bool
		maxFiles, maxBytes         int64
		wantBreached               bool
		wantMinFiles, wantMaxFiles int64
	}{
		{name: "counts only the worktree when the top .git is skipped", skipTopGit: true, maxFiles: 100, maxBytes: 1 << 20, wantMinFiles: 3, wantMaxFiles: 3},
		{name: "counts the git directory when it is not skipped", skipTopGit: false, maxFiles: 100, maxBytes: 1 << 20, wantMinFiles: 4, wantMaxFiles: 4},
		{name: "breaches on the file cap", skipTopGit: true, maxFiles: 2, maxBytes: 1 << 20, wantBreached: true, wantMinFiles: 1, wantMaxFiles: 3},
		{name: "breaches on the byte cap", skipTopGit: true, maxFiles: 100, maxBytes: 1, wantBreached: true, wantMinFiles: 1, wantMaxFiles: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := BoundedWalk(dir, tc.skipTopGit, tc.maxFiles, tc.maxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if st.Breached != tc.wantBreached {
				t.Fatalf("breached = %v, want %v (stats %+v)", st.Breached, tc.wantBreached, st)
			}
			if st.Files < tc.wantMinFiles || st.Files > tc.wantMaxFiles {
				t.Fatalf("files = %d, want between %d and %d", st.Files, tc.wantMinFiles, tc.wantMaxFiles)
			}
		})
	}
}
