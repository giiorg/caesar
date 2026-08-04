package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"

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
	// (0700: attempt workspaces and logs live under it). DataHostRoot is the
	// daemon-side bind mapping, not a path on this host, so it is never made.
	if err := os.MkdirAll(cfg.DataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("runner: create data root %s: %w", cfg.DataRoot, err)
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
		appGitSource{svc: assetsSvc}, runsSvc, opts, log)
}

// appHarnessResolver resolves a phase's harness from the assets context. The
// runs context never imports assets, so this translation lives here in the
// composition root. Entrypoint and Cmd are left nil: production runs the
// image's own default command (Plan 07's driver contract), not a
// control-plane-chosen one.
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
		CredentialID:     h.CredentialID,
	}, nil
}

// appCredentialOpener opens a stored secret through the AUDITED path
// (assets.UseCredential, spec §6 "every use audited") — never GetCredential.
type appCredentialOpener struct{ svc *assets.Service }

func (a appCredentialOpener) OpenCredential(ctx context.Context, id uuid.UUID, purpose string) ([]byte, error) {
	return a.svc.UseCredential(ctx, id, purpose)
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
