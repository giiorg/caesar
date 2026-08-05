// Package runner executes phase attempts as Docker containers: a staged
// workspace, an agent container with no route to the internet, and an
// egress gateway that is the only way out (spec §3). It is a runs
// subpackage because it implements that context's Executor and Admission
// ports; the daemon itself is reached through platform/dockerapi.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// Engine is the runner's port onto the container runtime; dockerapi.Client
// satisfies it, unit tests fake it. Ping is deliberately absent: liveness is
// the composition root's concern at wiring time, not the runner's per-attempt
// concern.
type Engine interface {
	ImageInspect(ctx context.Context, ref string) (dockerapi.ImageInspect, error)
	ImagePull(ctx context.Context, ref string) error
	NetworkCreate(ctx context.Context, name string, internal bool, labels map[string]string) (string, error)
	NetworkConnect(ctx context.Context, network, containerID string, aliases []string) error
	NetworkRemove(ctx context.Context, id string) error
	NetworkInspect(ctx context.Context, name string) (dockerapi.NetworkInspect, error)
	ContainerCreate(ctx context.Context, name string, cfg dockerapi.ContainerConfig, host dockerapi.HostConfig, networks map[string]dockerapi.EndpointSettings) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerWait(ctx context.Context, id string) (int64, error)
	ContainerKill(ctx context.Context, id, signal string) error
	ContainerRemove(ctx context.Context, id string, force bool) error
	ContainerInspect(ctx context.Context, idOrName string) (dockerapi.ContainerInspect, error)
	ContainerLogs(ctx context.Context, id string, follow bool) (io.ReadCloser, error)
	ContainerListByLabel(ctx context.Context, kv string) ([]dockerapi.ContainerSummary, error)
	NetworkListByLabel(ctx context.Context, kv string) ([]dockerapi.NetworkSummary, error)
}

// The production engine must keep satisfying the port without an adapter,
// and the runner must keep satisfying the orchestrator's executor port: both
// halves of the contract are pinned here rather than discovered at wiring
// time in the composition root.
var (
	_ Engine        = (*dockerapi.Client)(nil)
	_ runs.Executor = (*Runner)(nil)
)

// HarnessSpec is everything the runner needs to start one harness: which
// image and which provider it talks to. The credential is deliberately absent:
// the attempt carries its own binding, pinned when the request was built, and
// resolving one here would read the harness row as it is now instead.
// Entrypoint and Cmd are nil in production (the image's own defaults win);
// test resolvers script them.
type HarnessSpec struct {
	Kind             string
	ImageRef         string
	ProviderEndpoint string
	Entrypoint       []string
	Cmd              []string
}

// HarnessResolver resolves the harness a phase pinned. The runs context
// never imports assets, so the composition root supplies this.
type HarnessResolver interface {
	HarnessSpec(ctx context.Context, id uuid.UUID) (HarnessSpec, error)
}

// CredentialOpener opens a stored secret for one stated purpose. The plaintext
// never reaches the agent container — only the gateway holds it.
type CredentialOpener interface {
	OpenCredential(ctx context.Context, id uuid.UUID, purpose string) ([]byte, error)
}

// Lease is the runner's copy of the single-flight grant over a mutable
// credential (spec §2.1). Epoch is the takeover fence — reconciliation
// advances it before touching the container, so a paused worker and the
// reconciler that superseded it can be told apart — and SecretVersion is the
// version the grant materialized, which the sync-back CAS is compared
// against. Plain values only: the assets vocabulary never crosses into runs.
type Lease struct{ Epoch, SecretVersion int64 }

// SyncStatus is the outcome of writing an attempt's refreshed credential
// back. It mirrors the assets vocabulary by value. None of the four is an
// error: three of them mean the attempt's bytes were correctly not stored,
// and the caller releases its lease either way.
type SyncStatus string

const (
	SyncStatusApplied        SyncStatus = "applied"
	SyncStatusAlreadyApplied SyncStatus = "already_applied"
	SyncStatusCASLost        SyncStatus = "cas_lost"
	SyncStatusFenced         SyncStatus = "fenced"
)

// ErrLeaseBusy reports that another attempt holds the credential — including
// the fenced case, where a takeover has moved custody of this attempt's own
// lease to recovery. It is a wait, not a failure: the runner turns it into
// the zero-charge credential_busy outcome.
var ErrLeaseBusy = errors.New("runner: credential lease busy")

// CredentialLeaser is the runner's port onto the mutable-credential
// aggregate. The composition root adapts assets.Service onto it.
type CredentialLeaser interface {
	// Acquire returns the lease AND the secret snapshot it grants — one
	// atomic assets-side read: the bytes always correspond to
	// Lease.SecretVersion. Leased kinds never do a separate open.
	Acquire(ctx context.Context, credentialID uuid.UUID, ref string) (Lease, []byte, error) // ErrLeaseBusy when held elsewhere
	Heartbeat(ctx context.Context, credentialID uuid.UUID, ref string, epoch int64) error
	CommitSync(ctx context.Context, credentialID uuid.UUID, ref string, epoch, expectVersion int64, secret []byte) (SyncStatus, error)
	Release(ctx context.Context, credentialID uuid.UUID, ref string, epoch int64) error
	Takeover(ctx context.Context, credentialID uuid.UUID, ref string) (Lease, error)
	LeaseByAttempt(ctx context.Context, ref string) (uuid.UUID, Lease, bool, error)
	MarkAuthExpired(ctx context.Context, credentialID uuid.UUID) error
	RecordQuarantine(ctx context.Context, credentialID uuid.UUID, ref string) error
}

// CloneSpec is how the stage container reaches a repo. The credential travels
// as a header, never inside the URL: a token in a URL lands in reflogs, remote
// config, and process listings.
type CloneSpec struct {
	URL        string // credential-free always (token-in-URL banned)
	AuthHeader string // full header line value for http.extraheader, "" = none
	BindDir    string // non-empty = mount RO at /remote (test fixtures; URL then file:///remote/...)
}

// GitSource turns a repo reference into a clone the stage container can run.
type GitSource interface {
	CloneSpec(ctx context.Context, repo runs.RepoRef) (CloneSpec, error)
}

// EventSink is the runner's write path onto the run's event stream, for the
// things only the runtime knows (log truncation, egress denials).
type EventSink interface {
	AppendRuntimeEvent(ctx context.Context, ref runs.AttemptRef, kind string, payload map[string]any) error
}

// Options configures one runner. Every zero field takes the pinned default
// below, so a caller that supplies only the two image refs gets the shipped
// posture rather than a runner with zero-sized quotas.
type Options struct {
	DataRoot, DataHostRoot, CaesarImage, GitImage, EgressNetwork         string
	WorkspaceQuotaBytes, WorkspaceQuotaFiles, LogCapBytes, LogFloodBytes int64
	FreeBytesWatermark, MemoryBytes, NanoCPUs, PidsLimit                 int64
	QuotaPollInterval                                                    time.Duration
	ConnectAllow                                                         []string

	// testHostAccess maps host.docker.internal onto the host gateway for the
	// egress gateway container alone. It exists so the runner integration
	// suite can point an attempt at a fake provider running in the test
	// process; it is unexported, so no production wiring can set it, and no
	// agent container ever receives the mapping either way.
	testHostAccess bool

	// testLeaseHeartbeatInterval shortens the credential-lease heartbeat so a
	// test can observe several ticks without spending half a minute per tick.
	// Unexported for the same reason as testHostAccess: production always
	// runs at the pinned interval.
	testLeaseHeartbeatInterval time.Duration
}

// Pinned defaults (plan Global Constraints). Config may override them; a
// zero field never means "unlimited".
const (
	defaultDataRoot      = "/var/lib/caesar"
	defaultEgressNetwork = "caesar-egress"

	defaultWorkspaceQuotaBytes = 2 << 30 // 2 GiB
	defaultWorkspaceQuotaFiles = 200_000
	defaultLogCapBytes         = 16 << 20 // per stream
	defaultLogFloodBytes       = 64 << 20 // kill threshold
	defaultFreeBytesWatermark  = 10 << 30 // free on data_root
	defaultMemoryBytes         = 4 << 30
	defaultNanoCPUs            = 2_000_000_000 // 2.0 CPUs
	defaultPidsLimit           = 512
	defaultQuotaPollInterval   = 2 * time.Second
)

func (o Options) withDefaults() Options {
	if o.DataRoot == "" {
		o.DataRoot = defaultDataRoot
	}
	if o.DataHostRoot == "" {
		o.DataHostRoot = o.DataRoot
	}
	if o.EgressNetwork == "" {
		o.EgressNetwork = defaultEgressNetwork
	}
	if o.WorkspaceQuotaBytes == 0 {
		o.WorkspaceQuotaBytes = defaultWorkspaceQuotaBytes
	}
	if o.WorkspaceQuotaFiles == 0 {
		o.WorkspaceQuotaFiles = defaultWorkspaceQuotaFiles
	}
	if o.LogCapBytes == 0 {
		o.LogCapBytes = defaultLogCapBytes
	}
	if o.LogFloodBytes == 0 {
		o.LogFloodBytes = defaultLogFloodBytes
	}
	if o.FreeBytesWatermark == 0 {
		o.FreeBytesWatermark = defaultFreeBytesWatermark
	}
	if o.MemoryBytes == 0 {
		o.MemoryBytes = defaultMemoryBytes
	}
	if o.NanoCPUs == 0 {
		o.NanoCPUs = defaultNanoCPUs
	}
	if o.PidsLimit == 0 {
		o.PidsLimit = defaultPidsLimit
	}
	if o.QuotaPollInterval == 0 {
		o.QuotaPollInterval = defaultQuotaPollInterval
	}
	return o
}

// Runner is the Docker-backed executor. freeBytes is a field so the
// watermark logic is testable without a nearly-full disk.
type Runner struct {
	engine    Engine
	harnesses HarnessResolver
	creds     CredentialOpener
	leaser    CredentialLeaser
	git       GitSource
	events    EventSink
	opts      Options
	log       *slog.Logger
	uid, gid  int
	freeBytes func(string) (int64, error)
}

// New validates the posture the whole isolation story rests on before any
// attempt runs: the control plane is not root (decision 4 — attempt
// containers run as this process's uid:gid, and uid 0 would hand the agent
// root on every bind mount), both images are pinned by the operator, and the
// flood threshold is not below the truncation cap.
func New(engine Engine, harnesses HarnessResolver, creds CredentialOpener, leaser CredentialLeaser,
	git GitSource, events EventSink, opts Options, log *slog.Logger,
) (*Runner, error) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		return nil, errors.New("runner: refusing to start as root: attempt containers run as the control plane's uid:gid")
	}
	opts = opts.withDefaults()
	if opts.CaesarImage == "" {
		return nil, errors.New("runner: caesar image is required")
	}
	if opts.GitImage == "" {
		return nil, errors.New("runner: git image is required")
	}
	if opts.LogFloodBytes < opts.LogCapBytes {
		return nil, fmt.Errorf("runner: log flood threshold %d is below the per-stream log cap %d", opts.LogFloodBytes, opts.LogCapBytes)
	}
	return &Runner{
		engine:    engine,
		harnesses: harnesses,
		creds:     creds,
		leaser:    leaser,
		git:       git,
		events:    events,
		opts:      opts,
		log:       log,
		uid:       uid,
		gid:       gid,
		freeBytes: diskFreeBytes,
	}, nil
}
