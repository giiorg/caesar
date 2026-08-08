package fake

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// Publisher is runs.GitPublisher against LOCAL bare repositories. It does the
// real work — gitops.Publish, the same function production calls — because the
// only interesting question about a publication is what ends up on the remote,
// and a fake that answers from a map could not be wrong about it.
//
// Every call gets its own directory and removes it: gitops.Publish clones into
// <dir>/publish and sets HOME=<dir>, so a second call on one directory fails
// at `git clone`.
type Publisher struct {
	mu       sync.Mutex
	remotes  map[uuid.UUID]string
	pushes   int
	created  int
	lastReq  runs.PushRequest
	crashing bool
	// holdBeforePush blocks a call BEFORE anything reaches the remote, which
	// is the shape a wedged worker has: the lease expires with nothing
	// published and the intent still 'pending'.
	holdBeforePush chan struct{}
}

func NewPublisher(remotes map[uuid.UUID]string) *Publisher {
	return &Publisher{remotes: copyHeads(remotes)}
}

// Remote registers the local bare repository a repo id resolves to.
func (p *Publisher) Remote(repoID uuid.UUID, bare string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.remotes[repoID] = bare
}

// input is shared by both methods for the same reason the production adapter
// shares one prepare: ExpectedHead's entire purpose is to reproduce what
// PushPatch would do, so the two may not differ about any input.
func (p *Publisher) input(req runs.PushRequest) (gitops.PublishInput, error) {
	p.mu.Lock()
	bare, ok := p.remotes[req.Repo.ID]
	p.mu.Unlock()
	if !ok {
		return gitops.PublishInput{}, fmt.Errorf("fake publisher: no remote registered for repo %s", req.Repo.ID)
	}
	return gitops.PublishInput{
		CloneURL: bare, BaseSHA: req.BaseSHA, Branch: req.Branch,
		TreeHash: req.TreeHash, Patch: req.Patch, Message: req.Message,
		AuthorName: req.AuthorName, AuthorEmail: req.AuthorEmail,
		Date: req.Date, ExpectHead: req.ExpectHead,
	}, nil
}

func (p *Publisher) scratch() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "caesar-fake-publish-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func (p *Publisher) PushPatch(ctx context.Context, req runs.PushRequest) (runs.PushResult, error) {
	in, err := p.input(req)
	if err != nil {
		return runs.PushResult{}, err
	}
	p.mu.Lock()
	hold := p.holdBeforePush
	p.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			// Nothing reached the remote, so this is honestly a non-start.
			// A bare error would default-deny to a permanent internal_error
			// and kill the run on the first injection.
			return runs.PushResult{}, runs.OutcomeError{Outcome: runs.Outcome{
				Class: runs.OutcomeTransient, Code: runs.CodePublishNotStarted,
				Message: "fake publisher: held before push, then cancelled",
			}}
		}
	}

	dir, cleanup, err := p.scratch()
	if err != nil {
		return runs.PushResult{}, err
	}
	defer cleanup()
	rep, err := gitops.Publish(ctx, dir, in, nil)
	if err != nil {
		// gitops speaks its own TypedError; runs.ClassifyOutcome matches only
		// runs.OutcomeError and default-denies everything else to a permanent
		// internal_error. Translating here is what keeps a conflicted marker
		// haltable and a rejected push retryable.
		return runs.PushResult{}, gitops.AsOutcomeError(err)
	}

	p.mu.Lock()
	p.pushes++
	p.lastReq = req
	if rep.Created {
		p.created++
	}
	crash := p.crashing
	p.mu.Unlock()

	if crash {
		// The branch is on the remote and no receipt exists: the one window
		// that makes a publication non-idempotent if recovery is wrong.
		return runs.PushResult{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomeTransient, Code: runs.CodePublishResumable,
			Message: "fake publisher: crashed after pushing",
		}}
	}
	return runs.PushResult{HeadSHA: rep.HeadSHA, TreeHash: rep.TreeHash, Created: rep.Created}, nil
}

func (p *Publisher) ExpectedHead(ctx context.Context, req runs.PushRequest) (string, error) {
	in, err := p.input(req)
	if err != nil {
		return "", err
	}
	dir, cleanup, err := p.scratch()
	if err != nil {
		return "", err
	}
	defer cleanup()
	head, err := gitops.ExpectedPublicationHead(ctx, dir, in, nil)
	return head, gitops.AsOutcomeError(err)
}

// CrashAfterPush makes PushPatch do its real work and then fail.
func (p *Publisher) CrashAfterPush(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.crashing = on
}

// HangBeforePush wedges every call before it causes anything, until the
// returned release is called. Release at cleanup too: a wedged publisher
// leaks a goroutine -race will surface in an unrelated test.
func (p *Publisher) HangBeforePush() (release func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := make(chan struct{})
	p.holdBeforePush = ch
	return sync.OnceFunc(func() {
		p.mu.Lock()
		p.holdBeforePush = nil
		p.mu.Unlock()
		close(ch)
	})
}

// PushCount is how many calls reached gitops.Publish and it returned. It
// counts adoptions too — a call that found the branch already at its own
// commit did reach the remote — which is why CreatedCount exists beside it.
func (p *Publisher) PushCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pushes
}

// CreatedCount is how many calls actually CREATED the branch. Exactly one
// creation per publication is the property the crash-and-adopt path exists to
// hold, and no other counter can see it.
func (p *Publisher) CreatedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.created
}

// LastRequest is the request the last successful push was built from, so a
// test can replay it through ExpectedHead and check the two agree.
func (p *Publisher) LastRequest() runs.PushRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastReq
}
