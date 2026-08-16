package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
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
	"github.com/giiorg/caesar/backend/internal/webui"
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

// msgListening is logged once the listener is bound, and it is where the port
// is written down: with addr ":0" the configured address is not the address,
// so an operator — and the integration fixture's awaitListening — reads the
// real one from this line. Named once so that renaming the message is a single
// edit the fixture follows, rather than eight integration tests going red over
// a string.
//
// Only the message can be shared this way. The "addr" key below is read back
// through a struct tag, and a struct tag cannot be a const.
const msgListening = "http server listening"

// serveOptions are overrides no production path can set.
//
// It is UNEXPORTED and constructed only inside package app, so no config file,
// flag or environment variable can reach it — the language is the enforcement,
// not a convention. It exists for one reason: a gate can never be a root node
// (workflows.validateConsumption requires it to examine exactly one plan or
// patch), so reaching phase_runs.status = 'waiting_decision' means an agentic
// phase must complete, and the web E2E lane substitutes the agent CONTAINER so
// the orchestrator's real gate materialization can be observed. Everything
// below that substitution is production code.
type serveOptions struct {
	// executor, when non-nil, replaces the executor the configuration selected.
	executor runs.Executor
	// listener, when non-nil, is served INSTEAD of binding cfg.Server.Addr.
	//
	// It exists because of a genuine ordering deadlock, not for convenience.
	// Routes is constructed with cfg.Server.AppOrigin before net.Listen runs
	// (below), and httpx.CheckOrigin compares a browser's Origin header to
	// that string by EXACT equality on every unsafe method. With addr ":0" the
	// port is the operating system's answer, known only after the bind — so a
	// test that lets the OS choose cannot state the origin the browser will
	// send, and a login POST from http://127.0.0.1:<port> gets a 403 from
	// production middleware that is behaving exactly as designed.
	//
	// Handing the bound listener in inverts the order: the test binds, reads
	// the real port, sets AppOrigin to match, and only then boots. A fixed port
	// would be the other way out and is rejected — the lane must not collide
	// with a developer's own server or with a parallel package under test.
	listener net.Listener
}

// runServe boots the control plane: connect, migrate (advisory-locked),
// serve HTTP until ctx is cancelled, then drain within the shutdown timeout.
//
// This is the only path cmd/caesar reaches, and it passes an empty
// serveOptions — see runServeWith.
func runServe(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	return runServeWith(ctx, cfg, log, serveOptions{})
}

func runServeWith(ctx context.Context, cfg config.Config, log *slog.Logger, opts serveOptions) error {
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
		forgeSvc, publisher, principalSource{svc: svc}, runs.Params{
			Snapshot: runs.DefaultSnapshot(),
			Labels:   runsLabelSource{assets: assetsSvc, workflows: workflowsSvc},
		})

	// Realtime (spec §4): ONE tailer, ONE hub, ONE Streamer for the whole
	// process, started before anything can serve a request against them.
	//
	// ONE grant resolver, for the same reason the forge endpoints are built
	// once: it answers both "what may this principal see" at stream open and
	// the re-check that runs for the life of every subscription, and the two
	// answers diverging is a stream that shows what the opener was allowed
	// rather than what the principal is allowed now.
	streamGrants := realtimeGrants{svc: svc}
	rt := startRealtime(ctx, pool, cfg.Database.URL, cfg.Realtime, streamGrants, log)
	// On every return path. The shutdown block below calls it in its proper
	// place and Stop is idempotent, so this only covers the early returns —
	// where the alternative is a tailer goroutine and a dedicated database
	// connection outliving the function that made them.
	defer rt.Stop()

	// ONE realtime surface behind two URLs. The attention stream is its own
	// endpoint; the per-run stream is reached through the runs endpoint that
	// already owns that URL, behind runs/api's StreamServer port, because runs
	// and realtime are peers and neither may name the other.
	streams := realtimeapi.New(rt.streamer, svc, streamGrants, log)
	ops = append(ops, runsapi.Operations(runsapi.New(runsSvc, svc, svc, svc, streams))...)
	ops = append(ops, realtimeapi.Operations(streams)...)

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
	if opts.executor != nil {
		// Last word, and deliberately after the runner branch: a lane that set
		// both is a test that meant this one.
		executor = opts.executor
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
		// No Addr: Serve takes the listener below, and only ListenAndServe
		// would read this field. Leaving it set would be a second answer to
		// "which address is this" that nothing consults.
		Handler:           Routes(log, pool, ops, cfg.Server.AppOrigin, webui.Handler()),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout deliberately unset: SSE streams (Plan 10) are
		// long-lived responses.
	}

	// Bound the listener HERE rather than inside ListenAndServe, for two
	// reasons. A port that cannot be bound is a boot failure and now reads as
	// one, instead of arriving on errCh after the process has already claimed
	// to be listening. And with the port left to the operating system —
	// addr ":0", which the integration tests use so they never collide — the
	// configured address is not the address, so the log line would otherwise
	// record a port nothing is on.
	//
	// An injected listener replaces the bind and nothing else: the log line,
	// the deferred Close and everything after are untouched, so it travels the
	// identical path — including msgListening, which is still where readiness
	// is announced.
	ln := opts.listener
	if ln == nil {
		ln, err = net.Listen("tcp", cfg.Server.Addr)
		if err != nil {
			return fmt.Errorf("http server: listen %s: %w", cfg.Server.Addr, err)
		}
	}
	// Shutdown closes it on the ordinary path; this is for the returns that
	// never get there.
	defer func() { _ = ln.Close() }()

	errCh := make(chan error, 1)
	go func() {
		log.Info(msgListening, "addr", ln.Addr().String(), "version", buildinfo.Version)
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case err := <-rt.failed:
		// A tailer that could not seed its cursor never started, and a process
		// that serves streams which snapshot and then go silent forever is
		// worse than one that exits — see runTailer.
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", cfg.Server.ShutdownTimeout.Std().String())
	// REALTIME FIRST, and this is the part that gets written the wrong way
	// round. Shutdown waits for every active connection to finish, and an SSE
	// connection does not finish on its own: it is a response that stays open
	// until something ends it. Shutting the server down first would therefore
	// wait out the whole ShutdownTimeout on every deploy with a stream open.
	// Closing the hub ends those subscriptions, the handlers return, and
	// Shutdown then completes promptly. Stop's own internal order is
	// load-bearing too — see there.
	//
	// It does not close EVERY stream: one still in its snapshot or catch-up
	// phase is not watching the subscription channel yet and only finds out
	// when it reaches the live loop, so Shutdown can still wait on a
	// catching-up client, bounded by ShutdownTimeout.
	rt.Stop()
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
