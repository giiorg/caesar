package fake

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// Forge is the deterministic code host: scripted intake heads, branch probes
// answered from a real local bare repository, and an in-memory pull-request
// store.
//
// It is a POINTER type behind a constructor, and that is not style. The
// injectors below are set from a test goroutine and read from orchestrator
// worker goroutines, and the counters are read back while workers are still
// running; a value receiver would copy the mutex and the maps, silently drop
// every injection, and race.
type Forge struct {
	mu sync.Mutex
	// heads is what intake pins as a run's base SHA.
	heads map[uuid.UUID]string
	// remotes maps a repo id to a local bare repository, so BranchHead
	// answers what is really there rather than what a script says.
	remotes map[uuid.UUID]string

	prs    map[string][]runs.PullRequestRef // head branch -> requests on it
	nextPR int64

	openPRs        int
	createAttempts int
	branchProbes   int

	crashAfterPR bool
	failPRTimes  int
	failPRCode   string
	// hiddenListings makes the next N FindPullRequests answer empty even
	// though a request exists. It models a forge that has accepted a create
	// and not yet published it to its list endpoint, which is the ONLY way a
	// second driver's pre-create probe can see nothing and its create still
	// be refused.
	//
	// hideAfterCreate is armed by the injector and moved into hiddenListings
	// when a create SUCCEEDS. Arming hiddenListings directly would hide the
	// creating driver's own pre-create probe instead, and the second driver
	// would then see the request and adopt it through the ordinary find —
	// never reaching the create conflict the injector exists to stage.
	hiddenListings  int
	hideAfterCreate int
	// holdPR blocks the first OpenPullRequest between the request existing on
	// the forge and the caller learning about it. That gap is the create-PR
	// race's whole window: the winner has published and its intent still
	// says 'pushed'.
	holdPR chan struct{}
}

func NewForge(heads map[uuid.UUID]string) *Forge {
	return &Forge{
		heads:   copyHeads(heads),
		remotes: map[uuid.UUID]string{},
		prs:     map[string][]runs.PullRequestRef{},
		nextPR:  1,
	}
}

func copyHeads(in map[uuid.UUID]string) map[uuid.UUID]string {
	out := make(map[uuid.UUID]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Head pins what ResolveHead answers for a repo. The constructor copies its
// map rather than aliasing it — the map is read from orchestrator goroutines
// under this mutex, and a caller holding the original would race it — so a
// test that learns its repo id after construction sets it here.
func (f *Forge) Head(repoID uuid.UUID, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[repoID] = sha
}

// Remote registers the local bare repository that IS this repo, so branch
// probes and pull-request head SHAs read the real thing.
func (f *Forge) Remote(repoID uuid.UUID, bare string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remotes[repoID] = bare
}

func (f *Forge) ResolveHead(_ context.Context, repo runs.RepoRef) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, ok := f.heads[repo.ID]
	if !ok {
		return "", fmt.Errorf("fake forge: no head for repo %s", repo.ID)
	}
	return sha, nil
}

func (f *Forge) BranchHead(_ context.Context, repo runs.RepoRef, branch string) (string, bool, error) {
	f.mu.Lock()
	f.branchProbes++
	bare, ok := f.remotes[repo.ID]
	f.mu.Unlock()
	if !ok {
		return "", false, fmt.Errorf("fake forge: no remote registered for repo %s", repo.ID)
	}
	return bareBranchHead(bare, branch)
}

func (f *Forge) OpenPullRequest(ctx context.Context, repo runs.RepoRef, spec runs.PullRequestSpec) (runs.PullRequestRef, error) {
	f.mu.Lock()
	f.createAttempts++
	if f.failPRTimes > 0 {
		f.failPRTimes--
		code := f.failPRCode
		f.mu.Unlock()
		return runs.PullRequestRef{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomeTransient, Code: code, Message: "fake forge: scripted pull-request failure",
		}}
	}
	if len(f.prs[spec.Head]) > 0 {
		// What a real forge answers to a second request on one head. It is a
		// statement about the world, not about who is entitled to it.
		f.mu.Unlock()
		return runs.PullRequestRef{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodePublishConflict,
			Message: fmt.Sprintf("fake forge: a pull request for head %s already exists", spec.Head),
		}}
	}
	bare, ok := f.remotes[repo.ID]
	if !ok {
		f.mu.Unlock()
		return runs.PullRequestRef{}, fmt.Errorf("fake forge: no remote registered for repo %s", repo.ID)
	}
	f.mu.Unlock()

	head, _, err := bareBranchHead(bare, spec.Head)
	if err != nil {
		return runs.PullRequestRef{}, err
	}

	f.mu.Lock()
	if len(f.prs[spec.Head]) > 0 {
		// Re-checked after the head read: the lock was released for it, and
		// two concurrent creates must not both reserve a number.
		f.mu.Unlock()
		return runs.PullRequestRef{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodePublishConflict,
			Message: fmt.Sprintf("fake forge: a pull request for head %s already exists", spec.Head),
		}}
	}
	ref := runs.PullRequestRef{
		Number: f.nextPR, URL: fmt.Sprintf("https://fake.forge/%s/pull/%d", repo.Slug, f.nextPR),
		HeadSHA: head, Base: spec.Base, State: "open",
	}
	f.nextPR++
	f.prs[spec.Head] = append(f.prs[spec.Head], ref)
	f.openPRs++
	f.hiddenListings, f.hideAfterCreate = f.hideAfterCreate, 0
	crash, hold := f.crashAfterPR, f.holdPR
	f.holdPR = nil // only the FIRST create is held
	f.mu.Unlock()

	if hold != nil {
		// The request exists from here on; only its receipt is withheld.
		select {
		case <-hold:
		case <-ctx.Done():
			return runs.PullRequestRef{}, runs.OutcomeError{Outcome: runs.Outcome{
				Class: runs.OutcomeTransient, Code: runs.CodePublishResumable,
				Message: "fake forge: held after opening a pull request, then cancelled",
			}}
		}
	}
	if crash {
		// The request is open and the caller never learns its number — the
		// lost-receipt window an unconditional FindPullRequests closes.
		return runs.PullRequestRef{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomeTransient, Code: runs.CodeForgeUnavailable,
			Message: "fake forge: crashed after opening the pull request",
		}}
	}
	return ref, nil
}

func (f *Forge) FindPullRequests(_ context.Context, _ runs.RepoRef, headBranch string) ([]runs.PullRequestRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hiddenListings > 0 {
		f.hiddenListings--
		return nil, nil
	}
	return slices.Clone(f.prs[headBranch]), nil
}

// CrashAfterPR makes every create land on the forge and then fail before the
// caller can record it.
func (f *Forge) CrashAfterPR(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.crashAfterPR = on
}

// FailPRTimes refuses the next n creates with a typed transient outcome. The
// TYPE is load-bearing: ClassifyOutcome is default-deny, so a bare error would
// become a permanent internal_error and kill the run on the first injection.
func (f *Forge) FailPRTimes(n int, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPRTimes, f.failPRCode = n, code
}

// HoldFirstPullRequest blocks the first create between the request existing on
// the forge and the caller returning. Releasing is idempotent and must also
// happen at cleanup: a held publisher goroutine outlives the test otherwise.
func (f *Forge) HoldFirstPullRequest() (release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{})
	f.holdPR = ch
	return sync.OnceFunc(func() { close(ch) })
}

// HideListingsAfterCreate makes the n listings that follow the next accepted
// create answer empty.
func (f *Forge) HideListingsAfterCreate(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hideAfterCreate = n
}

// SeedPullRequest puts somebody else's request on a head branch, so a
// publication can find its marker already carrying one it cannot claim.
func (f *Forge) SeedPullRequest(headBranch string, ref runs.PullRequestRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs[headBranch] = append(f.prs[headBranch], ref)
}

// CreateAttemptCount is how many creates were ATTEMPTED, refusals included.
//
// It is the only way to see whether the machine asked before it acted: a forge
// that refuses a duplicate reaches the same verdict either way, so no outcome
// assertion can tell "we found the stranger's request and stopped" from "we
// tried to open one and were told no".
func (f *Forge) CreateAttemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createAttempts
}

// OpenPRCount is how many creates the forge ACCEPTED, refusals excluded.
func (f *Forge) OpenPRCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openPRs
}

// PullRequestCount is how many requests exist on a head branch. It is the
// assertion that separates "adopted" from "opened a duplicate": both end with
// a published intent, and only this tells them apart.
func (f *Forge) PullRequestCount(branch string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prs[branch])
}

func (f *Forge) BranchProbeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.branchProbes
}
