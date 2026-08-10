package gitops

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// publishTestDate pins the commit timestamps so a rebuilt publication hashes
// identically. Tests that care about reproducibility rely on this; tests that
// do not still need it, because an unpinned date is a refusal.
var publishTestDate = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

func TestPublish_CreatesTheBranchAndCommitsWithTheHumanAsAuthor(t *testing.T) {
	remote, base := initRemote(t) // bare repo + one seed commit
	dir := t.TempDir()
	patch := addFilePatch("NEW.md", "hello\n")
	// Every git command Publish runs sees a non-UTC local zone, so a date
	// layout that dropped its offset would be interpreted in that zone and the
	// %aI assertions below would catch it on a UTC host too. The zone is a
	// POSIX TZ string, not "Asia/Tokyo": a name needs a zoneinfo database, and
	// on an image without one git falls back to UTC WITHOUT erroring — the pin
	// would evaporate, the mutant would survive, and the test would stay green.
	pinGitLocalZone(t, "JST-9")

	rep, err := Publish(t.Context(), dir, PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		TreeHash:   approvedTree(t, remote, base, patch),
		Patch:      patch,
		Message:    "Add NEW.md\n\ncaesar-run: r1\n",
		AuthorName: "alice", AuthorEmail: "alice@example.com",
		Date: publishTestDate,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Created {
		t.Error("a branch that did not exist must be reported as created")
	}
	if rep.HeadSHA == "" || rep.HeadSHA == base {
		t.Fatalf("head = %q; want a new commit on top of %q", rep.HeadSHA, base)
	}

	// The remote really has it, and the attribution is the pinned split.
	author := gitOut(t, remote, "log", "-1", "--format=%an <%ae>", rep.HeadSHA)
	if author != "alice <alice@example.com>" {
		t.Errorf("author = %q; the requester is the AUTHOR (spec §6 requester principal)", author)
	}
	committer := gitOut(t, remote, "log", "-1", "--format=%cn <%ce>", rep.HeadSHA)
	if committer != "caesar <caesar@invalid>" {
		t.Errorf("committer = %q; caesar is the COMMITTER", committer)
	}
	// Both dates are pinned AND carry their zone. A git commit object stores
	// `<epoch> <offset>` and the offset is hashed, so a stamp that leaves the
	// zone to the host makes the sha host-dependent — which breaks the adopt
	// comparison the whole crash-recovery design rests on.
	//
	// Assert `--date=raw`, which prints the stored `<epoch> <offset>` verbatim.
	// Do NOT go back to %aI/%cI: those render the offset, and the rendering is
	// git-version-dependent — 2.39 prints "+00:00" where 2.54 prints "Z" for
	// the same bytes, so a passing %aI assertion on the dev host reds CI while
	// the commit is byte-identical. Raw is both stricter and version-stable.
	if got := gitOut(t, remote, "log", "-1", "--format=%ad", "--date=raw", rep.HeadSHA); got != "1786104000 +0000" {
		t.Errorf("author date = %q; the stamp must carry its zone or the sha is host-dependent", got)
	}
	if got := gitOut(t, remote, "log", "-1", "--format=%cd", "--date=raw", rep.HeadSHA); got != "1786104000 +0000" {
		t.Errorf("committer date = %q; both identities are pinned", got)
	}
	// The tree the caller is handed back is the commit's own, not the base's
	// and not an echo of the input — settlement persists this value.
	if want := gitOut(t, remote, "rev-parse", rep.HeadSHA+"^{tree}"); rep.TreeHash != want {
		t.Errorf("report tree = %q, want the published commit's tree %q", rep.TreeHash, want)
	}
	if body := gitOut(t, remote, "log", "-1", "--format=%B", rep.HeadSHA); !strings.Contains(body, "caesar-run: r1") {
		t.Errorf("the provenance trailers must survive: %q", body)
	}
	if strings.Contains(gitOut(t, remote, "log", "-1", "--format=%B", rep.HeadSHA), "Co-authored-by") {
		t.Error("no machine co-author trailer is emitted (baked decision 7)")
	}
}

func TestPublish_TheSameInstantInAnotherZoneRebuildsTheSameCommit(t *testing.T) {
	// The other half of reproducibility, and the one no wall-clock sleep can
	// see: the sha must be a function of the INSTANT, never of the Location the
	// caller's time.Time happens to carry. Without the normalization the same
	// durable timestamp read back through a different zone rebuilds a different
	// commit, the adopt comparison misses, and a crash-after-push becomes a
	// permanent publish_conflict on a branch that is genuinely ours.
	remote, base := initRemote(t)
	patch := addFilePatch("NEW.md", "hello\n")
	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		TreeHash: approvedTree(t, remote, base, patch),
		Patch:    patch, Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}
	utc, err := ExpectedPublicationHead(t.Context(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	in.Date = publishTestDate.In(time.FixedZone("elsewhere", 5*3600))
	other, err := ExpectedPublicationHead(t.Context(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if utc != other {
		t.Fatalf("the same instant rebuilt as %s in UTC and %s elsewhere; the sha must not depend on the caller's Location", utc, other)
	}
}

func TestPublish_IsIdempotentWhenTheBranchIsAlreadyOurs(t *testing.T) {
	// The crash-after-push case: the branch exists at exactly the head we
	// would have created. Publishing again must adopt it, not fail and not
	// create a second commit.
	//
	// The two calls are deliberately separated by a wall-clock second. If the
	// commit date were not pinned, the rebuilt commit would hash differently,
	// the adopt comparison would miss, and the second call would
	// force-with-lease a replacement over the first commit — the exact
	// duplication this whole state machine exists to prevent. Anything that
	// makes this test pass only when it runs fast is not idempotency.
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	patch := addFilePatch("NEW.md", "hello\n")
	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		TreeHash:   approvedTree(t, remote, base, patch),
		Patch:      patch,
		Message:    "Add NEW.md\n",
		AuthorName: "alice", AuthorEmail: "alice@example.com",
		Date: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
	}
	first, err := Publish(t.Context(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)

	in.ExpectHead = first.HeadSHA
	second, err := Publish(t.Context(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatalf("re-publishing an identical, already-pushed branch must adopt it: %v", err)
	}
	if second.HeadSHA != first.HeadSHA {
		t.Fatalf("head moved from %s to %s; the second publish created a new commit", first.HeadSHA, second.HeadSHA)
	}
	if second.Created {
		t.Error("an adopted branch was not created by this call")
	}
	// The adopted report is the same publication, so it carries the same tree —
	// settlement persists this from either arm.
	if second.TreeHash != first.TreeHash {
		t.Errorf("adopted tree = %q; want the tree the create reported, %q", second.TreeHash, first.TreeHash)
	}
	if got := gitOut(t, remote, "rev-list", "--count", branch); got != "2" {
		t.Errorf("branch has %s commits, want 2 (seed + one publication)", got)
	}
}

func TestPublish_RefusesWhenTheBranchExistsAtSomethingElse(t *testing.T) {
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	// Somebody else put a commit on our marker.
	pushForeignCommit(t, remote, branch, base)
	patch := addFilePatch("NEW.md", "x\n")

	_, err := Publish(t.Context(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		TreeHash: approvedTree(t, remote, base, patch),
		Patch:    patch, Message: "m",
		AuthorName: "alice", AuthorEmail: "alice@example.com", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) {
		t.Fatalf("want a typed outcome, got %v", err)
	}
	if te.Outcome.Code != runs.CodePublishConflict || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("outcome = %+v; want a PERMANENT publish_conflict — a marker we cannot prove is ours is a human's problem", te.Outcome)
	}
}

func TestPublish_AForeignMarkerIsNeverOverwrittenEvenWhenItLooksAdoptable(t *testing.T) {
	// The sharpest version of the ownership question. A foreign commit sits
	// on our marker, and the caller — a recovery path that observed that same
	// sha — passes it as ExpectHead. Every signal available short of rebuilding
	// the commit says "this is the branch you pushed last time". It is not.
	//
	// An implementation that treats an observed head as a lease value will
	// force-push over this commit and pass every other test in this file.
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	foreign := pushForeignCommit(t, remote, branch, base)
	patch := addFilePatch("NEW.md", "hello\n")

	_, err := Publish(t.Context(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		TreeHash: approvedTree(t, remote, base, patch),
		Patch:    patch, Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
		ExpectHead: foreign, // the caller genuinely saw this
	}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("err = %v; want publish_conflict: an observed sha is evidence, not permission", err)
	}
	if got := gitOut(t, remote, "rev-parse", branch); got != foreign {
		t.Fatalf("the foreign commit was replaced: branch is at %s, was %s", got, foreign)
	}
}

func TestPublish_ABranchCreatedBetweenProbeAndPushIsRefused(t *testing.T) {
	// The create path's race. Publish probes, finds nothing, builds its
	// commit — and in that window somebody creates the branch AT THE BASE.
	// A plain push would fast-forward it and report success, silently taking
	// over a ref we never proved was ours; only an atomic create-only lease
	// refuses.
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	patch := addFilePatch("NEW.md", "hello\n")

	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		TreeHash: approvedTree(t, remote, base, patch),
		Patch:    patch, Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}
	// afterPrepare runs once the commit is built and before the push — the
	// same runGitFn seam the credential test uses.
	createBranchAfterPrepare(t, remote, branch, base)

	_, err := Publish(t.Context(), t.TempDir(), in, nil)
	if err == nil {
		t.Fatal("a branch that appeared under this publication must not be pushed onto")
	}
	// The ref is untouched: still the interloper's, still at base.
	if got := gitOut(t, remote, "rev-parse", branch); got != base {
		t.Errorf("branch moved to %s; the push fast-forwarded a ref it did not own", got)
	}
}

func TestPublish_ExpectedPublicationHeadRebuildsWithoutTouchingTheRemote(t *testing.T) {
	// Expired-run settlement needs the sha a publication WOULD push while
	// holding no authority to push it. Same answer, no ref written.
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	patch := addFilePatch("NEW.md", "hello\n")
	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		TreeHash: approvedTree(t, remote, base, patch),
		Patch:    patch, Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}

	want, err := ExpectedPublicationHead(t.Context(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, remote, "branch", "--list", "caesar/*"); got != "" {
		t.Fatalf("reconstructing a publication must write nothing; remote has %q", got)
	}

	got, err := Publish(t.Context(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != want {
		t.Fatalf("reconstruction said %s but publishing produced %s; settlement would misjudge every receiptless branch", want, got.HeadSHA)
	}
}

func TestPublish_ExpectHeadMismatchIsAConflictNotAForce(t *testing.T) {
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	patch := addFilePatch("NEW.md", "hello\n")
	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		TreeHash: approvedTree(t, remote, base, patch),
		Patch:    patch, Message: "m",
		AuthorName: "alice", AuthorEmail: "alice@example.com", Date: publishTestDate,
	}
	first, _ := Publish(t.Context(), t.TempDir(), in, nil)
	pushForeignCommit(t, remote, branch, first.HeadSHA) // the branch moved under us

	in.ExpectHead = first.HeadSHA
	_, err := Publish(t.Context(), t.TempDir(), in, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("err = %v; want publish_conflict — never a force push", err)
	}
	// And the foreign commit is still there.
	if gitOut(t, remote, "rev-parse", branch) == first.HeadSHA {
		t.Fatal("the publish overwrote somebody else's commit")
	}
}

func TestPublish_RefusesAPatchThatDoesNotApply(t *testing.T) {
	remote, base := initRemote(t)
	_, err := Publish(t.Context(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		TreeHash: unreachedTree,                              // the apply fails before any tree is written
		Patch:    addFilePatch("README.md", "conflicting\n"), // README.md already exists
		Message:  "m", AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) {
		t.Fatalf("want a typed outcome, got %v", err)
	}
	if te.Outcome.Class != runs.OutcomePermanent || te.Outcome.Code != runs.CodeInvalidOutput {
		t.Fatalf("outcome = %+v; a patch that does not apply is permanently invalid", te.Outcome)
	}
	if strings.Contains(te.Outcome.Message, "conflicting") {
		t.Error("git's message quotes hunks from a patch we did not author; it must not be echoed")
	}
}

func TestPublish_VerifiesTheTreeHashItWasPromised(t *testing.T) {
	// The authorization pinned a tree hash. If applying the approved patch
	// produces a different tree, the artifact and the authorization have
	// diverged and NOTHING may be pushed.
	remote, base := initRemote(t)
	_, err := Publish(t.Context(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		Patch: addFilePatch("NEW.md", "hello\n"), TreeHash: "0000000000000000000000000000000000000000",
		Message: "m", AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("err = %v; want publish_conflict on a tree-hash mismatch", err)
	}
	if got := gitOut(t, remote, "branch", "--list", "caesar/*"); got != "" {
		t.Fatalf("a mismatch must push nothing; remote has %q", got)
	}
}

func TestPublish_LeavesNoCredentialBehind(t *testing.T) {
	remote, base := initRemote(t)
	authEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic c3VwZXJzZWNyZXQ=",
	}
	dir := t.TempDir()
	patch := addFilePatch("NEW.md", "hello\n")
	if _, err := Publish(t.Context(), dir, PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		TreeHash: approvedTree(t, remote, base, patch),
		Patch:    patch, Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, authEnv); err != nil {
		t.Fatal(err)
	}
	if found := grepTree(t, dir, "c3VwZXJzZWNyZXQ="); len(found) != 0 {
		t.Fatalf("the credential persisted in: %v", found)
	}
}

// The only thing between a userinfo clone URL and a token in .git/config's
// remote.origin.url — which the config scan's key list does not match and
// redact does not know about, and which publishPrepare's base-sha refusal
// formats verbatim into an outcome message.
func TestPublish_TokenInURLIsRefusedBeforeAnyGitRuns(t *testing.T) {
	dir := t.TempDir()
	_, err := Publish(t.Context(), dir, PublishInput{
		CloneURL: "https://x-access-token:sekret@github.com/a/b.git", BaseSHA: "abc",
		Branch: "caesar/" + uuid.NewString(), TreeHash: unreachedTree,
		Patch: addFilePatch("NEW.md", "hi\n"), Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v; want a permanent refusal", err)
	}
	// The code is checkCloneURL's own, shared with Stage, asserted as-is.
	if te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("outcome code = %s; want %s", te.Outcome.Code, runs.CodeStagingVerificationFailed)
	}
	if _, statErr := os.Stat(filepath.Join(dir, publishRepoDir)); !os.IsNotExist(statErr) {
		t.Fatal("no clone may happen with a token-bearing URL")
	}
	if strings.Contains(te.Outcome.Message, "sekret") {
		t.Fatalf("the refusal echoed the token: %q", te.Outcome.Message)
	}
}

// Publish must prove the checkout is credential-free BEFORE it writes anything
// of its own into it. Planted through the runGitFn seam because git never
// persists GIT_CONFIG_*-scoped auth on its own, so the fixture cannot produce
// the condition the guard exists for.
func TestPublish_AClonePersistingAuthIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	remote, base := initRemote(t)
	patch := addFilePatch("NEW.md", "hello\n")
	tree := approvedTree(t, remote, base, patch)
	plantAuthConfigAfterClone(t, "Authorization: Basic c3VwZXJzZWNyZXQ=")
	dir := t.TempDir()

	_, err := Publish(t.Context(), dir, PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		TreeHash: tree, Patch: patch, Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v; a checkout that cannot be proven credential-free is a permanent refusal", err)
	}
	if te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("outcome code = %s; want %s", te.Outcome.Code, runs.CodeStagingVerificationFailed)
	}
	if strings.Contains(te.Outcome.Message, "c3VwZXJzZWNyZXQ=") {
		t.Fatalf("the refusal leaked the value: %q", te.Outcome.Message)
	}
	if got := gitOut(t, remote, "branch", "--list", "caesar/*"); got != "" {
		t.Fatalf("nothing may be pushed after this refusal; remote has %q", got)
	}
	// The two above pin the refusal; these two pin its PLACEMENT, which is the
	// guard's whole point and the half a "does the helper work" unit test can
	// never reach. Without them the guard could be moved to the last statement
	// of publishPrepare — a full commit built inside a checkout that was never
	// proven credential-free — and nothing in the package would notice.
	if _, statErr := os.Stat(filepath.Join(dir, publishPatchFile)); !os.IsNotExist(statErr) {
		t.Error("the credential proof must come before anything of ours is written into the checkout")
	}
	if got := gitOut(t, filepath.Join(dir, publishRepoDir), "branch", "--list", "caesar/*"); got != "" {
		t.Errorf("the checkout already carries %q; the guard ran after the branch was created", got)
	}
}

func TestPublish_ABaseSHAThatIsGoneIsPermanentNotAPushRejection(t *testing.T) {
	// Without the cat-file probe the missing base surfaces from the checkout
	// instead, as a TRANSIENT push_rejected — so a permanent condition would be
	// retried max_attempts times. That is the misclassification, not the
	// failure itself; git refuses the checkout either way.
	remote, _ := initRemote(t)
	_, err := Publish(t.Context(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: strings.Repeat("a", 40),
		Branch: "caesar/" + uuid.NewString(), TreeHash: unreachedTree,
		Patch: addFilePatch("NEW.md", "hello\n"), Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Class != runs.OutcomePermanent || te.Outcome.Code != runs.CodeBaseSHAUnavailable {
		t.Fatalf("err = %v; want a permanent %s", err, runs.CodeBaseSHAUnavailable)
	}
}

func TestPublish_EveryRemoteCommandCarriesTheCredential(t *testing.T) {
	// Publish reaches the network three times — clone, the ls-remote
	// precondition probe, and push. A local bare repo authenticates none of
	// them, so a probe that silently ran without the credential would pass
	// every other test in this file and then fail on the first private repo
	// as an unexplained forge_unavailable. Assert the wiring directly.
	remote, base := initRemote(t)
	patch := addFilePatch("NEW.md", "hello\n")
	tree := approvedTree(t, remote, base, patch)
	seen := recordGitInvocations(t) // test hook: records argv + whether authEnv was present

	if _, err := Publish(t.Context(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		TreeHash: tree, Patch: patch, Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, []string{
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic c3VwZXJzZWNyZXQ=",
	}); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{"clone", "ls-remote", "push"} {
		if !seen.ranWithAuth(cmd) {
			t.Errorf("%s talks to the remote and must receive the credential", cmd)
		}
	}
	// And the converse: nothing else does.
	for _, cmd := range []string{"apply", "commit", "write-tree", "checkout", "cat-file", "rev-parse"} {
		if seen.ranWithAuth(cmd) {
			t.Errorf("%s never reaches the network and must not see the credential", cmd)
		}
	}
}

// The refusals the rest of this file takes for granted. Every literal above
// pins a Date, an approved tree and a real branch name, so without these
// nothing would notice if the guards were deleted.
func TestPublish_RefusalsThatHappenBeforeAnythingIsMaterialized(t *testing.T) {
	remote, base := initRemote(t)
	good := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		TreeHash: unreachedTree,
		Patch:    addFilePatch("NEW.md", "hello\n"), Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}
	tests := []struct {
		name  string
		tweak func(in *PublishInput)
	}{
		{"an unpinned date is what makes a publication irreproducible", func(in *PublishInput) { in.Date = time.Time{} }},
		{"an unpinned tree hash is an unverifiable publication", func(in *PublishInput) { in.TreeHash = "" }},
		{"an empty branch is not a ref name", func(in *PublishInput) { in.Branch = "" }},
		{"a branch that would parse as an option is not a ref name", func(in *PublishInput) { in.Branch = "--upload-pack=touch /tmp/pwned" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := good
			tc.tweak(&in)
			dir := t.TempDir()
			_, err := Publish(t.Context(), dir, in, nil)
			var te TypedError
			if !errors.As(err, &te) || te.Outcome.Class != runs.OutcomePermanent || te.Outcome.Code != runs.CodeInvalidOutput {
				t.Fatalf("err = %v; want a permanent %s", err, runs.CodeInvalidOutput)
			}
			if _, statErr := os.Stat(filepath.Join(dir, publishRepoDir)); !os.IsNotExist(statErr) {
				t.Error("a refusal this cheap must happen before anything is cloned")
			}
		})
	}
}

// The approved patch arrives base64-encoded like the staged one, but a patch
// that cannot be decoded here is invalid OUTPUT — nothing is being staged.
func TestPublish_AnUndecodablePatchIsInvalidOutputNotStagingFailed(t *testing.T) {
	remote, base := initRemote(t)
	_, err := Publish(t.Context(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		TreeHash: unreachedTree, Patch: "not base64!!", Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Class != runs.OutcomePermanent || te.Outcome.Code != runs.CodeInvalidOutput {
		t.Fatalf("err = %v; want a permanent %s", err, runs.CodeInvalidOutput)
	}
}

// Both of decodePublishPatch's caps, each named by the message it produces —
// the two are indistinguishable by class and code, so only the wording can
// tell which one refused, and a raised or deleted cap changes the wording.
func TestPublish_TheApprovedPatchCapsAreTypedAndNameTheBound(t *testing.T) {
	oversizeWire := strings.Repeat("A", 4*(maxInputPatchBytes/3+8))
	tests := []struct {
		name  string
		patch string
		want  string
	}{
		{
			// Refused from the wire length alone, without ever materializing
			// the decoded bytes.
			name:  "a wire form too large to be worth decoding",
			patch: oversizeWire,
			want:  fmt.Sprintf("about %d bytes decoded, past the %d byte cap", base64.StdEncoding.DecodedLen(len(oversizeWire)), maxInputPatchBytes),
		},
		{
			// One byte past the real bound, which only the decoded length sees.
			name:  "a patch one byte past the decoded cap",
			patch: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("p"), maxInputPatchBytes+1)),
			want:  fmt.Sprintf("is %d bytes decoded, past the %d byte cap", maxInputPatchBytes+1, maxInputPatchBytes),
		},
	}
	remote, base := initRemote(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Publish(t.Context(), t.TempDir(), PublishInput{
				CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
				TreeHash: unreachedTree, Patch: tc.patch, Message: "m",
				AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
			}, nil)
			var te TypedError
			if !errors.As(err, &te) || te.Outcome.Class != runs.OutcomePermanent || te.Outcome.Code != runs.CodeInvalidOutput {
				t.Fatalf("err = %v; want a permanent %s", err, runs.CodeInvalidOutput)
			}
			if !strings.Contains(te.Outcome.Message, tc.want) {
				t.Fatalf("message = %q; want it to name the bound it enforced (%q)", te.Outcome.Message, tc.want)
			}
		})
	}
}
