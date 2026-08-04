package gitops

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
