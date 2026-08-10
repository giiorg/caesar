package gitops

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// The publish checkout's layout inside the working directory.
const (
	publishRepoDir   = "publish"
	publishPatchFile = "publish.patch"
)

// runGitFn is the seam the publish tests swap: `Publish` reaches the network
// three times and the wiring of the credential onto exactly those three is
// not observable against a local bare repo any other way. Deliberately not
// shared with stage.go or extract.go — one mutable hook across three files is
// a race under -race.
//
// The same reasoning bounds its use here: this var is only safe because no
// test in this package calls t.Parallel, so it is written from the running
// test's own goroutine and restored LIFO by t.Cleanup. The first t.Parallel
// added anywhere in `gitops` turns it into a data race.
var runGitFn = runGit

// PublishInput is one publication. Patch is the base64 wire form the patch
// artifact carries (Plan 07 decision: json.Marshal silently corrupts
// non-UTF-8 patch bytes that still apply cleanly).
type PublishInput struct {
	CloneURL string
	BaseSHA  string
	Branch   string
	// TreeHash is what the approval pinned. Applying the approved patch must
	// reproduce exactly this tree or nothing is pushed. Required: an empty one
	// is refused, never treated as "skip the check".
	TreeHash string
	Patch    string
	Message  string
	// The requester is the AUTHOR; caesar is the committer (baked decision 7).
	AuthorName, AuthorEmail string
	// Date is the timestamp pinned into BOTH the author and committer
	// identities, and it is what makes this function reproducible.
	//
	// A git commit's SHA is a hash of tree, parent, message and the two
	// identities INCLUDING their timestamps. Letting git default them to the
	// wall clock means rebuilding the same publication one second later
	// produces a different SHA — so the adopt comparison below could never
	// match, and the crash-after-push retry would force-with-lease a
	// replacement commit over the one already on the remote. Pinning the date
	// to a durable value (the publish intent's created_at) makes the SHA a
	// pure function of the inputs, which is what "recovery adopts" requires.
	Date time.Time
	// ExpectHead is the remote precondition. "" means the branch must not
	// exist; a value means it must be exactly that commit. There is no third
	// mode, and there is never a force.
	ExpectHead string
}

type PublishReport struct {
	HeadSHA  string
	TreeHash string
	// Created is false when the branch was already at the head this call
	// would have produced — the crash-after-push case, adopted idempotently.
	Created bool
}

// preparedPublication is everything Publish knows before it decides whether
// to write: the commit it built, and what the marker currently points at.
type preparedPublication struct {
	// HeadSHA is the commit this publication produces. Reproducible across
	// calls because the date is pinned, which is the whole basis of "is the
	// branch on the remote ours".
	HeadSHA  string
	TreeHash string
	// RemoteHead is evidence about the marker, never permission to write it.
	RemoteHead string
	RemoteHas  bool
	// Dir is the checkout, so a caller that only wanted the sha can drop it.
	Dir string
}

// Publish materializes a fresh trusted checkout of the remote at BaseSHA,
// applies the approved patch, commits with the pinned attribution and pushes
// Branch under an expected-remote-SHA precondition.
//
// Nothing the agent controlled is read here. The clone is the REMOTE's own
// content at a pinned commit — no agent-written .git, no hooks, no
// diff.external — and the patch is the canonical diff extraction already
// produced and validated. Repo code is never executed. That is why this runs
// in the control-plane process where staging and extraction do not (baked
// decision 8).
//
// Structurally it is `publishPrepare` — clone, verify, apply, commit, all of
// which cause nothing outside `dir` — followed by the remote decision and at
// most one push. Splitting there is not tidiness: it is what lets expired-run
// settlement ask "what would this have pushed" through the SAME code, so
// "ours" has exactly one definition.
func Publish(ctx context.Context, dir string, in PublishInput, authEnv []string) (PublishReport, error) {
	credential := credentialFrom(authEnv)
	env := baseEnv(dir)

	p, err := publishPrepare(ctx, dir, in, authEnv)
	if err != nil {
		return PublishReport{}, err
	}

	// 7. NOW decide, with the rebuilt commit in hand. There are exactly three
	// possibilities and the sha answers all of them:
	//
	//   - the marker carries exactly this commit → a prior attempt pushed and
	//     lost its receipt. Adopt it; push nothing.
	//   - the marker carries something else → we cannot prove it is ours, and
	//     the commit we would push is demonstrably not what is there. Halt.
	//   - the marker is absent → create it.
	//
	// `in.ExpectHead` is not consulted here. It survives on the struct only as
	// the caller's assertion about what it last saw, checked below for a
	// mismatch worth naming; it is never the thing that authorizes a write.
	if p.RemoteHas {
		if p.RemoteHead == p.HeadSHA {
			return PublishReport{HeadSHA: p.HeadSHA, TreeHash: p.TreeHash, Created: false}, nil
		}
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodePublishConflict,
			"branch %s is at %s, which is not this publication's commit %s; nothing here can prove it is ours",
			in.Branch, p.RemoteHead, p.HeadSHA)
	}
	if in.ExpectHead != "" {
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodePublishConflict,
			"branch %s was expected at %s but no longer exists", in.Branch, in.ExpectHead)
	}

	// 8. Create the branch under an ATOMIC create-only lease.
	//
	// A plain push is not sufficient and the difference is not theoretical: a
	// branch created at the base sha between the probe above and this push is
	// a fast-forward, so a plain push updates it and reports success —
	// silently adopting a ref we never proved was ours. --force-with-lease
	// with an EMPTY expected value means "only if this ref does not exist",
	// evaluated by the remote at push time, which closes that window.
	//
	// This is the only force this code may use, and it can never overwrite:
	// an empty lease fails against any existing ref.
	if _, err := runGitFn(ctx, p.Dir, slices.Concat(env, authEnv),
		"push", "--force-with-lease=refs/heads/"+in.Branch+":",
		"origin", "HEAD:refs/heads/"+in.Branch); err != nil {
		// Two very different failures share an exit code: the ref appeared
		// under us (the lease refused, a conflict a human resolves) and the
		// network blinked (worth a retry). We cannot tell them apart from the
		// exit code, so this is transient and bounded by max_attempts — and
		// the next attempt's rebuilt-sha comparison gives the exact answer.
		return PublishReport{}, typed(runs.OutcomeTransient, runs.CodePushRejected,
			"push %s: %s", in.Branch, redact(err.Error(), credential))
	}
	return PublishReport{HeadSHA: p.HeadSHA, TreeHash: p.TreeHash, Created: true}, nil
}

// publishPrepare is everything a publication does that causes nothing outside
// dir: clone at the pin, prove the checkout carries no credential, read what
// the marker points at, apply the approved patch, verify the tree and build
// the commit. It stops one step short of the push, which is what lets
// ExpectedPublicationHead share every definition with Publish.
func publishPrepare(ctx context.Context, dir string, in PublishInput, authEnv []string) (preparedPublication, error) {
	credential := credentialFrom(authEnv)
	repo := filepath.Join(dir, publishRepoDir)
	env := baseEnv(dir)

	if err := checkCloneURL(in.CloneURL); err != nil {
		return preparedPublication{}, err
	}
	if in.Branch == "" || strings.HasPrefix(in.Branch, "-") || strings.ContainsAny(in.Branch, " \t\r\n") {
		return preparedPublication{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"publish branch %q is not a usable ref name", in.Branch)
	}
	// The commit's reproducibility is an input property, so it is refused here
	// with the other cheap ones rather than after a full network clone — see
	// PublishInput.Date for why an unpinned date breaks recovery outright.
	if in.Date.IsZero() {
		return preparedPublication{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"publish requires a pinned commit date; an unpinned one makes the commit irreproducible")
	}
	// The approved tree is the same class of input property as the date — the
	// publication's integrity depends on it the way reproducibility depends on
	// the date — so it gets the same hard refusal rather than opting out
	// through a plain string's zero value. approved_patches.tree_hash is
	// `text NOT NULL` (backend/internal/platform/pg/schema.sql:372) and the
	// settlement path threads it as intent.TreeHash, so a tree hash is ALWAYS
	// available: an empty one is a caller bug, never a legitimate input.
	if in.TreeHash == "" {
		return preparedPublication{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"publish requires the approved tree hash; without it the applied patch is never checked against the approval")
	}

	// 1. A shallow-ish clone is not enough: the push needs the base commit's
	// full ancestry or the remote refuses a disconnected history. Clone the
	// default refs and check out the pin.
	if _, err := runGitFn(ctx, dir, slices.Concat(env, authEnv), "clone", "-q", in.CloneURL, repo); err != nil {
		return preparedPublication{}, typed(runs.OutcomeTransient, runs.CodeForgeUnavailable,
			"clone for publish: %s", redact(err.Error(), credential))
	}
	if _, err := runGitFn(ctx, repo, env, "cat-file", "-e", in.BaseSHA+"^{commit}"); err != nil {
		return preparedPublication{}, typed(runs.OutcomePermanent, runs.CodeBaseSHAUnavailable,
			"base sha %s is no longer in %s", in.BaseSHA, in.CloneURL)
	}

	// 2. Prove nothing persisted the credential into the clone's config
	// before we write anything of our own into it.
	if err := assertNoCredentialConfig(ctx, dir, repo); err != nil {
		return preparedPublication{}, err
	}

	// 3. Read what the marker points at. This is EVIDENCE, not permission.
	//
	// An earlier draft turned the observed head into ExpectHead and pushed
	// against it. That is backwards: observing a sha proves only that some
	// commit is there, and using it as a lease value converts "somebody else's
	// branch" into "a branch I am authorized to replace". The rebuilt commit
	// below is the only thing that can prove ownership, so the decision waits
	// until we have it.
	remoteHead, remoteHas, err := remoteBranchHead(ctx, repo, slices.Concat(env, authEnv), in.Branch, credential)
	if err != nil {
		return preparedPublication{}, err
	}

	// 4. Build the commit on a detached base, on a branch of our own name.
	if _, err := runGitFn(ctx, repo, env, "checkout", "-q", "-b", in.Branch, in.BaseSHA); err != nil {
		return preparedPublication{}, typed(runs.OutcomeTransient, runs.CodePushRejected,
			"create %s: %s", in.Branch, redact(err.Error(), credential))
	}
	patch, err := decodePublishPatch(in.Patch)
	if err != nil {
		return preparedPublication{}, err
	}
	if len(patch) > 0 {
		path := filepath.Join(dir, publishPatchFile)
		if err := os.WriteFile(path, patch, 0o600); err != nil {
			return preparedPublication{}, typed(runs.OutcomeTransient, runs.CodeInternalError,
				"write publish patch: %s", redact(err.Error(), credential))
		}
		if _, err := runGitFn(ctx, repo, env, "apply", "--index", "--whitespace=nowarn", path); err != nil {
			// git quotes hunks and paths out of a patch we did not author;
			// the caller gets the fact, never that text.
			return preparedPublication{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
				"the approved patch does not apply to %s", in.BaseSHA)
		}
	}

	// 5. Verify the tree BEFORE committing: a tree that is not what the human
	// approved must never become a commit, let alone a push.
	tree, err := runGitFn(ctx, repo, env, "write-tree")
	if err != nil {
		return preparedPublication{}, typed(runs.OutcomeTransient, runs.CodeInternalError,
			"write tree: %s", redact(err.Error(), credential))
	}
	treeHash := strings.TrimSpace(tree)
	// Unconditional: the empty TreeHash that used to opt out of this check is
	// refused at the top of this function.
	if treeHash != in.TreeHash {
		return preparedPublication{}, typed(runs.OutcomePermanent, runs.CodePublishConflict,
			"applying the approved patch produced tree %s, not the approved %s", treeHash, in.TreeHash)
	}

	// 6. Commit. The requester authors it; caesar commits it, and BOTH dates
	// are pinned so this call is reproducible — see PublishInput.Date. RFC3339
	// is one of git's accepted date formats and is unambiguous about the zone.
	stamp := in.Date.UTC().Format(time.RFC3339)
	commitEnv := slices.Concat(env, []string{
		"GIT_AUTHOR_NAME=" + in.AuthorName,
		"GIT_AUTHOR_EMAIL=" + in.AuthorEmail,
		"GIT_AUTHOR_DATE=" + stamp,
		"GIT_COMMITTER_DATE=" + stamp,
	})
	if _, err := runGitFn(ctx, repo, commitEnv, "commit", "-q", "--no-verify", "-m", in.Message); err != nil {
		// git's own words are echoed here where `apply`'s above are not: the
		// rule that silences `apply` is about hunk BODIES — arbitrary repo
		// content out of a patch we did not author — and a failing commit can
		// name paths from that patch but never quote their contents, while
		// being almost always a caller-construction bug (an empty message, an
		// empty patch) that is undiagnosable without them.
		return preparedPublication{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"commit the approved patch: %s", redact(err.Error(), credential))
	}
	head, err := runGitFn(ctx, repo, env, "rev-parse", "HEAD")
	if err != nil {
		return preparedPublication{}, typed(runs.OutcomeTransient, runs.CodeInternalError,
			"read publish head: %s", redact(err.Error(), credential))
	}

	return preparedPublication{
		HeadSHA:    strings.TrimSpace(head),
		TreeHash:   treeHash,
		RemoteHead: remoteHead,
		RemoteHas:  remoteHas,
		Dir:        repo,
	}, nil
}

// ExpectedPublicationHead reconstructs the commit a publication WOULD push and
// returns its sha, without touching the remote's refs.
//
// This is what makes a receiptless branch decidable. Recovery's question is
// always "is the thing on our marker the thing this authorization produces",
// and the only honest answer is the sha of the rebuilt commit — the pinned
// date (decision 7) is what makes that reproducible. The expired-run
// settlement path needs that answer while holding no authority to cause
// anything, so it needs the rebuild WITHOUT the push.
//
// It shares every step with Publish up to the commit, so the two can never
// disagree about what "ours" means.
func ExpectedPublicationHead(ctx context.Context, dir string, in PublishInput, authEnv []string) (string, error) {
	rep, err := publishPrepare(ctx, dir, in, authEnv)
	if err != nil {
		return "", err
	}
	return rep.HeadSHA, nil
}

// remoteBranchHead asks the remote (not the clone's stale remote-tracking
// refs) what the branch points at right now. ls-remote is the only read that
// is honest about a ref that moved between clone and push.
//
// env here MUST already carry authEnv: ls-remote is a network call to the same
// host clone and push talk to. Against a public remote — or the local bare
// repos this package's tests use — it succeeds without a credential, which is
// exactly why omitting it would pass every test and then fail on the first
// private repo, with prompts disabled, as an unexplained forge_unavailable.
func remoteBranchHead(ctx context.Context, repo string, env []string, branch, credential string) (string, bool, error) {
	out, err := runGitFn(ctx, repo, env, "ls-remote", "--exit-code", "origin", "refs/heads/"+branch)
	if err != nil {
		// exit 2 is ls-remote's "no matching ref", which is an answer.
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 2 {
			return "", false, nil
		}
		return "", false, typed(runs.OutcomeTransient, runs.CodeForgeUnavailable,
			"read remote ref %s: %s", branch, redact(err.Error(), credential))
	}
	sha, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
	if sha == "" {
		return "", false, nil
	}
	return sha, true, nil
}

// decodePublishPatch turns the approved patch's base64 wire form into its
// canonical bytes. It mirrors decodeInputPatch — same cap on the DECODED
// length, same wire form — but every refusal is CodeInvalidOutput rather than
// CodeStagingFailed: nothing is being staged here, and a patch this side
// cannot decode is a malformed artifact.
func decodePublishPatch(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	if n := base64.StdEncoding.DecodedLen(len(encoded)); n > maxInputPatchBytes+3 {
		// Refuse the obviously oversize without materializing it; the exact
		// check below is what the bound actually is.
		return nil, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"approved patch is about %d bytes decoded, past the %d byte cap", n, maxInputPatchBytes)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"approved patch is not the base64 wire form the patch artifact carries")
	}
	if len(raw) > maxInputPatchBytes {
		return nil, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"approved patch is %d bytes decoded, past the %d byte cap", len(raw), maxInputPatchBytes)
	}
	return raw, nil
}
