package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/egress"
)

const (
	validateLogFile        = "validate.log"
	validateGatewayLogFile = "validate-gateway.log"

	// defaultValidationTimeout bounds a validation command that names no
	// bound of its own. A build or a test suite is bounded work; the real
	// ceiling is the caller's (Plan 08), and this only exists so a zero
	// never means "forever".
	defaultValidationTimeout = 15 * time.Minute
)

// ValidationSpec is one validation run: a command over a directory, with a
// named set of hosts it may tunnel to. Plan 08 builds it; the runner only
// promises the command runs under the same isolation an agent does.
type ValidationSpec struct {
	Ref          runs.AttemptRef
	Dir          string // bind-mounted at /workspace; owned by the caller, never removed here
	Image        string
	Cmd          []string // the whole command line: it replaces the image's entrypoint and cmd
	ConnectAllow []string // hosts the validation gateway will tunnel to
	Timeout      time.Duration
}

// ValidationResult is what the command did. A failing build is an ExitCode,
// not an error: what a nonzero exit means about the change is Plan 08's
// decision, and this layer would only be guessing.
type ValidationResult struct {
	ExitCode  int64
	LogPath   string // relative to the data root, like every stored path reference
	Truncated bool
}

// RunValidation runs one command over a directory under the agent's own
// hardening profile, on a fresh internal network whose only other occupant is
// a CONNECT-only gateway.
//
// That gateway is Task 7's open mode: it holds no provider credential, so it
// needs no attempt token either, and a package manager that cannot be taught
// a proxy credential still reaches an allowlisted registry through
// HTTPS_PROXY. Everything it will not tunnel is refused at the door and
// logged, exactly as an attempt's egress is.
//
// Errors are infrastructure only. The caller's directory is an input: it is
// mounted, read, written by the command, and never removed here.
func (r *Runner) RunValidation(ctx context.Context, v ValidationSpec) (ValidationResult, error) {
	if v.Timeout <= 0 {
		v.Timeout = defaultValidationTimeout
	}
	logPath := filepath.Join(r.logsDir(v.Ref), validateLogFile)
	res := ValidationResult{LogPath: r.relPath(logPath)}
	if err := os.MkdirAll(r.logsDir(v.Ref), 0o700); err != nil {
		return res, transientf(runs.CodeInternalError, "create logs dir: %v", err)
	}
	defer r.teardownValidation(ctx, v.Ref)

	if err := r.ensureEgressNetwork(ctx); err != nil {
		return res, err
	}
	if _, err := r.engine.NetworkCreate(ctx, validateNetworkName(v.Ref), true, labels(v.Ref)); err != nil {
		return res, transientf(runs.CodeInternalError, "create network %s: %v", validateNetworkName(v.Ref), err)
	}

	streamCtx, stopStreams := context.WithCancel(context.WithoutCancel(ctx))
	var streams []*logStreamer
	defer func() { r.drainStreams(v.Ref, streams, stopStreams) }()

	gatewayID, err := r.startValidationGateway(ctx, v)
	if err != nil {
		return res, err
	}
	streams = append(streams, r.streamLogs(streamCtx, gatewayID,
		filepath.Join(r.logsDir(v.Ref), validateGatewayLogFile), v.Ref,
		streamOpts{stream: "validate-gateway"}))

	var out *logStreamer
	code, runErr := r.runToCompletion(ctx, validateName(v.Ref), dockerapi.ContainerConfig{
		Image: v.Image,
		// The command is the whole command line, so the image's own
		// entrypoint is replaced rather than prefixed onto it, and its cmd is
		// cleared rather than appended as arguments nobody asked for.
		Entrypoint: v.Cmd,
		Cmd:        []string{},
		Env: []string{
			"CAESAR_ATTEMPT=" + v.Ref.String(),
			"CAESAR_WORKSPACE=" + workspaceDir,
			"HTTPS_PROXY=" + brokerURL,
			"HTTP_PROXY=" + brokerURL,
			"NO_PROXY=",
			"HOME=" + agentHome,
			"PATH=" + agentPath,
		},
		User:       r.runAs(),
		WorkingDir: workspaceDir,
		Labels:     labels(v.Ref),
	}, r.hardenedHost([]string{r.hostPath(v.Dir) + ":" + workspaceDir},
		validateNetworkName(v.Ref), r.opts.MemoryBytes, r.opts.NanoCPUs, r.opts.PidsLimit),
		map[string]dockerapi.EndpointSettings{validateNetworkName(v.Ref): {}},
		v.Timeout,
		func(id string) {
			out = r.streamLogs(streamCtx, id, logPath, v.Ref, streamOpts{stream: "validate"})
			streams = append(streams, out)
		})

	// The gateway is stopped before the streams are drained so its final
	// usage line is in the file the caller reads.
	r.stopGateway(ctx, gatewayID)
	r.drainStreams(v.Ref, streams, stopStreams)
	streams = nil

	if runErr != nil {
		return res, transientf(runs.CodeInternalError, "validation container: %v", runErr)
	}
	res.ExitCode = code
	if out != nil {
		_, res.Truncated, _ = out.wait()
	}
	return res, nil
}

// startValidationGateway brings up the CONNECT-only half of a validation run:
// dual-homed like an attempt's gateway, but holding nothing — no provider
// endpoint, no credential, and therefore no token to protect (NewGateway
// refuses any other reading of an empty token digest).
func (r *Runner) startValidationGateway(ctx context.Context, v ValidationSpec) (string, error) {
	raw, err := json.Marshal(egress.Config{
		Attempt:      v.Ref.String(),
		TokenSHA256:  "",
		Provider:     egress.Provider{},
		ConnectAllow: v.ConnectAllow,
	})
	if err != nil {
		return "", transientf(runs.CodeInternalError, "encode egress config: %v", err)
	}

	net := validateNetworkName(v.Ref)
	host := r.hardenedHost(nil, net, gatewayMemoryBytes, gatewayNanoCPUs, gatewayPidsLimit)
	if r.opts.testHostAccess {
		host.ExtraHosts = []string{"host.docker.internal:host-gateway"}
	}
	id, err := r.engine.ContainerCreate(ctx, validateGatewayName(v.Ref), dockerapi.ContainerConfig{
		Image:  r.opts.CaesarImage,
		Cmd:    []string{"egress-proxy"},
		Env:    []string{config.EgressConfigEnv + "=" + string(raw)},
		User:   r.runAs(),
		Labels: labels(v.Ref),
	}, host, map[string]dockerapi.EndpointSettings{net: {Aliases: []string{gatewayAlias}}})
	if err != nil {
		return "", transientf(runs.CodeInternalError, "create validation gateway: %v", err)
	}
	if err := r.engine.NetworkConnect(ctx, r.opts.EgressNetwork, id, nil); err != nil {
		return "", transientf(runs.CodeInternalError, "attach validation gateway to %s: %v", r.opts.EgressNetwork, err)
	}
	if err := r.engine.ContainerStart(ctx, id); err != nil {
		return "", transientf(runs.CodeInternalError, "start validation gateway: %v", err)
	}
	return id, nil
}

// teardownValidation removes what a validation run created and nothing else.
// The caller's directory is not its to remove, and the attempt's own
// containers — which may be a live run on the same ref — are not either.
func (r *Runner) teardownValidation(ctx context.Context, ref runs.AttemptRef) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownTimeout)
	defer cancel()
	r.removeByName(ctx,
		[]string{validateName(ref), validateGatewayName(ref)},
		[]string{validateNetworkName(ref)})
}
