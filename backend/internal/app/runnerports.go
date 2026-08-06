package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner"
)

// dockerSocketPath is the daemon endpoint the runner speaks to. The Engine
// API client dials this unix socket; it is a fixed convention rather than a
// config knob because the control plane and daemon are co-located by design.
const dockerSocketPath = "/var/run/docker.sock"

// runnerDataDirs is the runner's on-disk tree under DataRoot, mirroring
// naming.go: per-attempt scratch, published logs, the one agent-writable
// credential directory, attempt manifests, and the unscanned streams that
// have not earned publication yet. Creating any of them creates DataRoot too,
// which is what AdmitClaim's free-space statfs needs.
var runnerDataDirs = []string{"attempts", "logs", "auth", "manifests", "quarantine"}

// runsSvc already writes the run's event stream, so it satisfies the runner's
// EventSink port directly — no adapter, only this compile-time assertion that
// the port and the method stay in lockstep.
var _ runner.EventSink = (*runs.Service)(nil)

// buildRunner assembles the Docker runner from config: a daemon client, the
// three composition-root adapters over assets (the runs context never imports
// assets), and runsSvc as the event sink. It is called from runServe only
// when cfg.Runner.Enabled.
func buildRunner(cfg config.Runner, assetsSvc *assets.Service, runsSvc *runs.Service, log *slog.Logger) (*runner.Runner, error) {
	// Nothing else creates DataRoot, and AdmitClaim statfs's it on every
	// claim to check the free-space watermark — so a fresh install refuses
	// every claim until the directory exists. Create the local tree here
	// (0700: attempt workspaces, logs and credentials live under it).
	// DataHostRoot is the daemon-side bind mapping, not a path on this host,
	// so it is never made.
	//
	// The five subdirectories are made up front rather than on demand because
	// they are the runner's on-disk contract, and an operator sizing or
	// mounting them separately should meet them on a fresh install instead of
	// after the first attempt of each kind happens to create one. The runner
	// still MkdirAlls its own as it writes them; this is the shape of the
	// tree, not a precondition it relies on.
	for _, dir := range runnerDataDirs {
		path := filepath.Join(cfg.DataRoot, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("runner: create data root %s: %w", path, err)
		}
	}

	engine := dockerapi.New(dockerSocketPath)
	opts := runner.Options{
		DataRoot:            cfg.DataRoot,
		DataHostRoot:        cfg.DataHostRoot,
		CaesarImage:         cfg.CaesarImage,
		GitImage:            cfg.GitImage,
		EgressNetwork:       cfg.EgressNetwork,
		WorkspaceQuotaBytes: cfg.WorkspaceQuotaBytes.Int64(),
		WorkspaceQuotaFiles: cfg.WorkspaceQuotaFiles,
		LogCapBytes:         cfg.LogCapBytes.Int64(),
		LogFloodBytes:       cfg.LogFloodBytes.Int64(),
		FreeBytesWatermark:  cfg.FreeBytesWatermark.Int64(),
		MemoryBytes:         cfg.MemoryBytes.Int64(),
		NanoCPUs:            int64(cfg.Cpus * 1e9),
		PidsLimit:           cfg.PidsLimit,
		QuotaPollInterval:   cfg.QuotaPollInterval.Std(),
		ConnectAllow:        cfg.ConnectAllow,
	}
	return runner.New(engine, appHarnessResolver{svc: assetsSvc}, appCredentialOpener{svc: assetsSvc},
		appCredentialLeaser{svc: assetsSvc}, appGitSource{svc: assetsSvc}, runsSvc, opts, log)
}

// appHarnessResolver resolves a phase's harness from the assets context. The
// runs context never imports assets, so this translation lives here in the
// composition root. The harness row's credential is deliberately not carried
// over: the attempt already pinned one. Entrypoint and Cmd are left nil:
// production runs the image's own default command (Plan 07's driver contract),
// not a control-plane-chosen one.
type appHarnessResolver struct{ svc *assets.Service }

func (a appHarnessResolver) HarnessSpec(ctx context.Context, id uuid.UUID) (runner.HarnessSpec, error) {
	h, err := a.svc.GetHarness(ctx, id)
	if err != nil {
		return runner.HarnessSpec{}, err
	}
	return runner.HarnessSpec{
		Kind:             string(h.Kind),
		ImageRef:         h.ImageRef,
		ProviderEndpoint: h.ProviderEndpoint,
	}, nil
}

// appCredentialOpener opens a stored secret through the AUDITED path
// (assets.UseCredentialVersioned, spec §6 "every use audited") — never
// GetCredential. The version rides back with the bytes because the attempt
// manifest records it: adoption after a crash compares what it holds against
// what the store holds now.
type appCredentialOpener struct{ svc *assets.Service }

func (a appCredentialOpener) OpenCredential(ctx context.Context, id uuid.UUID, purpose string) ([]byte, int64, error) {
	return a.svc.UseCredentialVersioned(ctx, id, purpose)
}

// appCredentialLeaser adapts the assets mutable-credential aggregate onto the
// runner's port. The translation is mechanical except for the busy answer:
// see Acquire.
type appCredentialLeaser struct{ svc *assets.Service }

var _ runner.CredentialLeaser = appCredentialLeaser{}

// Acquire takes the single-flight lease and the snapshot it grants.
//
// Only assets.ErrLeaseBusy becomes the runner's wait. Acquire reports two
// distinct conflicts — the lease is held elsewhere (which wraps the sentinel)
// and this attempt already holds a lease on a different credential (which
// does not, because it is a design error, not contention). Branching on the
// error class instead would turn that bug into a silent zero-charge retry
// loop that never resolves.
func (a appCredentialLeaser) Acquire(ctx context.Context, id uuid.UUID, ref string) (runner.Lease, []byte, error) {
	l, secret, err := a.svc.AcquireCredentialLease(ctx, id, ref)
	if errors.Is(err, assets.ErrLeaseBusy) {
		return runner.Lease{}, nil, runner.ErrLeaseBusy
	}
	if err != nil {
		return runner.Lease{}, nil, err
	}
	return leaseValue(l), secret, nil
}

func (a appCredentialLeaser) Heartbeat(ctx context.Context, id uuid.UUID, ref string, epoch int64) error {
	return a.svc.HeartbeatCredentialLease(ctx, id, ref, epoch)
}

func (a appCredentialLeaser) CommitSync(
	ctx context.Context, id uuid.UUID, ref string, epoch, expectVersion int64, secret []byte,
) (runner.SyncStatus, error) {
	status, err := a.svc.CommitCredentialSync(ctx, id, ref, epoch, expectVersion, secret)
	if err != nil {
		return "", err
	}
	return syncStatus(status)
}

func (a appCredentialLeaser) Release(ctx context.Context, id uuid.UUID, ref string, epoch int64) error {
	return a.svc.ReleaseCredentialLease(ctx, id, ref, epoch)
}

func (a appCredentialLeaser) Takeover(ctx context.Context, id uuid.UUID, ref string) (runner.Lease, error) {
	l, err := a.svc.TakeoverCredentialLease(ctx, id, ref)
	if err != nil {
		return runner.Lease{}, err
	}
	return leaseValue(l), nil
}

// LeaseByAttempt is the recovery-authority lookup: it answers from the lease
// row alone, so an attempt whose manifest never became durable is still
// resolvable to the credential it holds.
func (a appCredentialLeaser) LeaseByAttempt(ctx context.Context, ref string) (uuid.UUID, runner.Lease, bool, error) {
	l, ok, err := a.svc.CredentialLeaseByAttempt(ctx, ref)
	if err != nil || !ok {
		return uuid.Nil, runner.Lease{}, false, err
	}
	return l.CredentialID, leaseValue(l), true, nil
}

func (a appCredentialLeaser) MarkAuthExpired(ctx context.Context, id uuid.UUID) error {
	return a.svc.MarkCredentialAuthExpired(ctx, id)
}

func (a appCredentialLeaser) RecordQuarantine(ctx context.Context, id uuid.UUID, ref string) error {
	return a.svc.RecordCredentialQuarantine(ctx, id, ref)
}

func leaseValue(l assets.CredentialLease) runner.Lease {
	return runner.Lease{Epoch: l.Epoch, SecretVersion: l.SecretVersion}
}

// syncStatus maps the aggregate's vocabulary onto the runner's rather than
// converting the string: an unmapped status is a contract drift between the
// two halves, and the runner decides what to publish from this value.
func syncStatus(s assets.SyncStatus) (runner.SyncStatus, error) {
	switch s {
	case assets.SyncApplied:
		return runner.SyncStatusApplied, nil
	case assets.SyncAlreadyApplied:
		return runner.SyncStatusAlreadyApplied, nil
	case assets.SyncCASLost:
		return runner.SyncStatusCASLost, nil
	case assets.SyncFenced:
		return runner.SyncStatusFenced, nil
	}
	return "", fmt.Errorf("app: unknown credential sync status %q", s)
}

// appGitSource turns a repo reference into the stage container's clone. The
// forge token travels as a header, never inside the URL (a token-in-URL lands
// in reflogs and process listings). Self-hosted forge base URLs are Plan 08's
// forge port; until then only github/gitlab hosted URLs are constructed here.
type appGitSource struct{ svc *assets.Service }

func (a appGitSource) CloneSpec(ctx context.Context, repo runs.RepoRef) (runner.CloneSpec, error) {
	// Resolve (and validate) the forge before opening the credential: an
	// unsupported forge must fail without an audited secret use.
	url, err := cloneURL(repo.Forge, repo.Slug)
	if err != nil {
		return runner.CloneSpec{}, err
	}
	token, err := a.svc.UseCredential(ctx, repo.CredentialID, "clone:"+repo.Slug)
	if err != nil {
		return runner.CloneSpec{}, err
	}
	return runner.CloneSpec{
		URL:        url,
		AuthHeader: cloneAuthHeader(string(token)),
	}, nil
}

// cloneURL builds the hosted HTTPS clone URL for a forge/slug pair, rejecting
// any forge whose base URL is not yet pinned. Kept pure (no credential, no
// assets) so the github/gitlab switch is unit-testable on its own.
func cloneURL(forge, slug string) (string, error) {
	var base string
	switch forge {
	case "github":
		base = "https://github.com/"
	case "gitlab":
		base = "https://gitlab.com/"
	default:
		return "", fmt.Errorf("app: git source: unsupported forge %q (self-hosted base URLs arrive in Plan 08)", forge)
	}
	return base + slug + ".git", nil
}

// cloneAuthHeader renders the forge token as a Basic auth header. The token
// travels here, never inside the URL (a token-in-URL lands in reflogs and
// process listings).
func cloneAuthHeader(token string) string {
	auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return "Authorization: Basic " + auth
}
