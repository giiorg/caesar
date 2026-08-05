package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

	// The execution spine: real peer-context adapters, the honest
	// not-implemented executor and forge until Plans 06/08. identity.Service
	// serves as authenticator, authorizer, and grant source here too.
	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		unavailableForge{}, runs.Params{Snapshot: runs.DefaultSnapshot()})
	ops = append(ops, runsapi.Operations(runsapi.New(runsSvc, svc, svc, svc))...)

	// Plan 06: the real Docker runner when configured. Disabled by default —
	// production intake is refused until Plan 08's forge adapters, so the
	// honest stub stays the default posture (stubs never fake success).
	var executor runs.Executor = runs.NotImplementedExecutor{}
	var admission runs.Admission = runs.AlwaysAdmit{}
	runnerDone := make(chan struct{})
	close(runnerDone) // no-op wait unless a runner replaces it below
	if cfg.Runner.Enabled {
		rnr, err := buildRunner(cfg.Runner, assetsSvc, runsSvc, log)
		if err != nil {
			return fmt.Errorf("serve: runner: %w", err)
		}
		executor, admission = rnr, rnr
		runnerDone = make(chan struct{})
		go func() { defer close(runnerDone); _ = rnr.Run(ctx) }()
		log.Info("docker runner enabled", "data_root", cfg.Runner.DataRoot)
	}

	orch := runs.NewOrchestrator(runsSvc, executor, admission, runs.OrchestratorConfig{
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
