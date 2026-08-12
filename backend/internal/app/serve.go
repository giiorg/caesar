package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/giiorg/caesar/backend/internal/assets"
	assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"
	"github.com/giiorg/caesar/backend/internal/identity"
	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/buildinfo"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/logging"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	realtimeapi "github.com/giiorg/caesar/backend/internal/realtime/api"
	"github.com/giiorg/caesar/backend/internal/runs"
	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
	"github.com/giiorg/caesar/backend/internal/workflows"
	workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"
)

func runServeCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "caesar.json", "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	log, err := logging.New(stdout, cfg.Log.Level)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runServe(ctx, cfg, log); err != nil {
		log.Error("serve failed", "error", err)
		return 1
	}
	return 0
}

// probeWritable answers the only question a boot check can honestly ask about
// a directory: can THIS process create a file in it? Nothing else is
// equivalent — a stat tells you the mode bits and not whose uid they apply to,
// and a mode that looks right still loses to a read-only mount, a full
// filesystem, or a label the kernel enforces above the bits. So it creates a
// file and removes it.
//
// The dot prefix keeps the probe out of the way of the per-publication
// subdirectory listing, and it is removed on every path, including the one
// where the close fails.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".caesar-writable-")
	if err != nil {
		return fmt.Errorf("%s exists but this process cannot write in it: %w", dir, err)
	}
	name := f.Name()
	closeErr := f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("%s: could not remove the write probe %s: %w", dir, name, err)
	}
	if closeErr != nil {
		return fmt.Errorf("%s: write probe failed to close: %w", dir, closeErr)
	}
	return nil
}

// runServe boots the control plane: connect, migrate (advisory-locked),
// serve HTTP until ctx is cancelled, then drain within the shutdown timeout.
func runServe(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := pg.New(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pg.Migrate(ctx, pool, pg.Migrations()); err != nil {
		return err
	}
	log.Info("migrations applied")

	svc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL:     cfg.Auth.SessionIdleTTL.Std(),
		SessionAbsoluteTTL: cfg.Auth.SessionAbsoluteTTL.Std(),
	})

	if cfg.Security.MasterKey == "" {
		return errors.New("serve: security.master_key (or CAESAR_MASTER_KEY) is required")
	}
	keys, err := secrets.ParseKeyset(cfg.Security.MasterKey)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	assetsSvc := assets.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), keys)
	workflowsSvc := workflows.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}))

	// assetsSvc implements identityapi.ProjectChecker (grant endpoints 404
	// on unknown projects); identity.Service serves as authenticator,
	// authorizer, and grant source for the assets API (the authn ports,
	// wired here only).
	ops := identityapi.Operations(identityapi.New(svc, cfg.Auth.SessionAbsoluteTTL.Std(), assetsSvc))
	ops = append(ops, assetsapi.Operations(assetsapi.New(assetsSvc, svc, svc, svc))...)
	ops = append(ops, workflowsapi.Operations(workflowsapi.New(workflowsSvc, svc))...)

	// The execution spine: real peer-context adapters and the real forge.
	// identity.Service serves as authenticator, authorizer, and grant source
	// here too.
	//
	// ONE endpoints value builds the forge adapter, the publisher and the
	// runner's staging clone. Building it three times is how the API host and
	// the clone host drift apart, which is the silent self-hosted failure: the
	// run can read the repository and publishes nothing.
	endpoints := forgeEndpointsFrom(cfg.Forge)
	forgeSvc := newForgeAdapter(assetsSvc, endpoints,
		&http.Client{Timeout: cfg.Forge.RequestTimeout.Std()})

	// config.Load validates publish_work_dir as a non-empty absolute path, so
	// an empty one means this Config never went through Load — the shape the
	// integration tests build by hand. Falling back keeps them booting without
	// pinning a path they never asked for. The directory is made HERE, at boot,
	// and not lazily on first publication: a work dir nobody can write is a
	// misconfiguration, and discovering it at the first publication is
	// discovering it at the worst possible moment.
	workDir := cfg.Forge.PublishWorkDir
	if workDir == "" {
		workDir = os.TempDir()
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return fmt.Errorf("serve: publish work dir: %w", err)
	}
	// And MkdirAll is not the question. It returns nil on a directory that
	// already exists, whatever its owner or mode, so on the deployment shape
	// this check exists for — the packaged default /var/lib/caesar/publish,
	// pre-created by a package or a mount, for a caesar process that is not
	// root — creation always succeeds and answers nothing. Only writing
	// answers it, so write.
	if err := probeWritable(workDir); err != nil {
		return fmt.Errorf("serve: publish work dir: %w", err)
	}
	publisher := gitPublisher{assets: assetsSvc, workDir: workDir, endpoints: endpoints}

	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		forgeSvc, publisher, principalSource{svc: svc}, runs.Params{Snapshot: runs.DefaultSnapshot()})
	// The nil StreamServer is Task 11's to replace: the adapter it needs does
	// not exist until the hub, tailer and streamer have a lifecycle. Until
	// then `Accept: text/event-stream` on the events endpoint answers 500
	// rather than quietly falling back to the poll (runs/api).
	ops = append(ops, runsapi.Operations(runsapi.New(runsSvc, svc, svc, svc, nil))...)
	// Same nil, same task, for the same reason. identity.Service is the real
	// authenticator and grant resolver here — only the Streamer is missing, so
	// only the Streamer is nil.
	ops = append(ops, realtimeapi.Operations(realtimeapi.New(nil, svc, realtimeGrants{svc: svc}, log))...)

	// Plan 06: the real Docker runner when configured. Disabled by default —
	// it needs a Docker daemon, built images and a writable data root, so the
	// honest stub stays the default posture (stubs never fake success).
	var executor runs.Executor = runs.NotImplementedExecutor{}
	var admission runs.Admission = runs.AlwaysAdmit{}
	runnerDone := make(chan struct{})
	close(runnerDone) // no-op wait unless a runner replaces it below
	if cfg.Runner.Enabled {
		rnr, err := buildRunner(cfg.Runner, endpoints, assetsSvc, runsSvc, log)
		if err != nil {
			return fmt.Errorf("serve: runner: %w", err)
		}
		executor, admission = rnr, rnr
		runnerDone = make(chan struct{})
		go func() { defer close(runnerDone); _ = rnr.Run(ctx) }()
		log.Info("docker runner enabled", "data_root", cfg.Runner.DataRoot)
	}

	orch := runs.NewOrchestrator(runsSvc, executor, admission, runsAssetSource{svc: assetsSvc}, runs.OrchestratorConfig{
		Workers:           cfg.Orchestrator.Workers,
		PollInterval:      cfg.Orchestrator.PollInterval.Std(),
		LeaseTTL:          cfg.Orchestrator.LeaseTTL.Std(),
		HeartbeatInterval: cfg.Orchestrator.HeartbeatInterval.Std(),
		OutboxRetention:   cfg.Orchestrator.OutboxRetention.Std(),
	}, log)
	orchDone := make(chan struct{})
	go func() { defer close(orchDone); _ = orch.Run(ctx) }()

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           Routes(log, pool, ops, cfg.Server.AppOrigin),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout deliberately unset: SSE streams (Plan 10) are
		// long-lived responses.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.Server.Addr, "version", buildinfo.Version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", cfg.Server.ShutdownTimeout.Std().String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.Std())
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	<-orchDone
	<-runnerDone
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
