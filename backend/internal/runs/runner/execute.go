package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

const (
	// The trusted containers are bounded work on a pinned commit; ten
	// minutes is a hang, not a slow clone.
	stageTimeout   = 10 * time.Minute
	extractTimeout = 10 * time.Minute

	// gatewayStopGrace is how long the gateway gets to flush its final
	// egress.summary line after TERM before it is killed outright.
	gatewayStopGrace = 5 * time.Second

	// agentCorpseGrace is how long the daemon gets to finish reaping a killed
	// agent before it is inspected. Without it the failure snapshot can
	// describe a container that is still technically running, and report the
	// exit code of a process that had not exited yet.
	agentCorpseGrace = 5 * time.Second

	// streamDrainGrace is how long a log stream gets to end on its own once
	// its container is gone, before the follow is cancelled out from under it.
	streamDrainGrace = 2 * time.Second

	teardownTimeout = 60 * time.Second

	// maxDenialEvents caps the denial events one attempt may write. An agent
	// in a retry loop can generate refusals without limit; the event stream
	// is a narrative, not a metric.
	maxDenialEvents = 20

	remoteMount    = "/remote"
	agentLogFile   = "agent.log"
	gatewayLogFile = "gateway.log"
)

// Execute runs one phase attempt as the four-container pipeline (spec §3):
// a trusted container stages the workspace, the gateway comes up as the only
// route off the attempt's internal network, the agent runs against the
// workspace alone, and a second trusted container extracts the patch. Every
// return path tears the whole set down.
//
// The agent's only bind is the workspace. base.git — the trusted mirror
// extraction diffs against — stays outside its reach on purpose: a writable
// mirror would make the patch whatever the agent says it is.
func (r *Runner) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	ref := req.Ref
	if err := os.MkdirAll(r.attemptDir(ref), 0o700); err != nil {
		return runs.ExecutionResult{}, transientf(runs.CodeInternalError, "create attempt dir: %v", err)
	}
	// The streams land in quarantine, not in the published logs directory:
	// what an agent printed cannot be scanned completely until the credential
	// obligation has concluded (spec §3), and publication is what moves them.
	if err := os.MkdirAll(r.quarantineDir(ref), 0o700); err != nil {
		return runs.ExecutionResult{}, transientf(runs.CodeInternalError, "create quarantine dir: %v", err)
	}
	defer r.teardown(ctx, ref)

	// A harness that has vanished between the orchestrator's request build
	// and here is a registry mutation mid-flight, not a hiccup: retrying
	// finds the same nothing.
	hs, err := r.harnesses.HarnessSpec(ctx, req.HarnessID)
	if err != nil {
		return runs.ExecutionResult{}, permanentf(runs.CodeHarnessError, "resolve harness %s: %v", req.HarnessID, err)
	}
	spec, err := r.buildSpec(ctx, req, hs)
	if err != nil {
		return runs.ExecutionResult{}, err
	}
	// The driver is re-derived rather than carried on the spec: RunSpec stays
	// plain data, and buildSpec has already refused any kind that has no row.
	d, err := driverFor(hs.Kind)
	if err != nil {
		return runs.ExecutionResult{}, permanentf(runs.CodeHarnessError, "%v", err)
	}

	// Past this point the attempt may hold a credential lease, and a fenced
	// heartbeat has to be able to cut the whole pipeline short: the takeover
	// means the reconciler owns this attempt now.
	ctx, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()

	// The credential resolves before anything else, because everything after
	// it depends on which bytes this attempt actually holds. It is the one the
	// request pinned, never one re-resolved off today's harness row: a registry
	// edit mid-attempt must not swap the secret under a running phase.
	//
	// A credential a running attempt can itself replace is taken exclusively
	// first, before anything usage-bearing starts. Contention is somebody
	// else's turn rather than this attempt's failure, so it returns as a wait
	// and nothing has been spent when it does. Such a credential is then never
	// opened a second time: the acquire transaction handed back both the lease
	// and the bytes of exactly the version it recorded, and that pairing is
	// what lets sync-back's CAS tell an operator's rotation apart from the
	// CLI's own refresh.
	leased := needsLease(req.Credential)
	var g grant
	// The manifest variable exists before the lease does so the deferred
	// conclusion can read it: concludeCredential is the single choke point
	// where the credential obligation ends, and every exit path — a failure
	// between acquire and agent start included — funnels through it once.
	var m attemptManifest
	concluded := false
	conclude := func() syncConclusion {
		concluded = true
		return r.concludeCredential(ctx, ref, m, req.Credential.ID)
	}
	stopHeartbeat := func() {}
	if leased {
		lease, secret, err := r.acquireCredential(ctx, ref, req.Credential)
		if err != nil {
			return runs.ExecutionResult{}, err
		}
		g = grant{secret: secret, version: lease.SecretVersion, epoch: lease.Epoch}
		// Until manifestFor pins the full manifest, the granted epoch is all
		// a conclusion needs: an exit in between still releases at the right
		// fence, with nothing on disk to read back.
		m.Epoch = lease.Epoch
		// Registered before the heartbeat so it runs after it, and guarded so
		// no path concludes twice: the conclusion is the last thing this
		// attempt does to the credential, on early exits as on full runs.
		defer func() {
			if !concluded {
				conclude()
			}
		}()
		stopHeartbeat = r.startLeaseHeartbeat(ctx, req.Credential.ID, ref, g.epoch, cancelAttempt)
		defer stopHeartbeat()
	} else {
		// Unleased credentials are infrastructure here: an unavailable secret
		// store says nothing about this attempt's work.
		secret, version, err := r.creds.OpenCredential(ctx, req.Credential.ID, "attempt:"+ref.String())
		if err != nil {
			return runs.ExecutionResult{}, transientf(runs.CodeStagingFailed, "open provider credential: %v", err)
		}
		g = grant{secret: secret, version: version}
	}
	spec.ProviderCredential = string(g.secret)

	// The redaction set is assembled the moment the bytes are in hand, and it
	// is the single source of truth for every scan this attempt runs: the
	// patch, the declared artifacts, the logs, and the failure snapshot. The
	// raw credential enters whole — a brokered binding's is an API key, and the
	// driver's extractor describes an auth file (Task 6's per-kind rule) — and
	// the driver's fields join it under materialized custody. What the CLI may
	// refresh mid-run is unknown until the conclusion reads it back, so the set
	// is completed there and nothing publishes before then (spec §3).
	set := newSensitiveSet()
	if short := set.addRaw(g.secret, []byte(spec.BrokerToken)); short > 0 {
		// Below the floor the credential still redacts — it is secret by
		// definition — but it now blanks every incidental occurrence of itself.
		// Operator-caused, and loud rather than silent.
		r.log.Warn("runner.short_credential", "attempt", ref.String(),
			"values", short, "floor", minSecretLen)
	}
	// withheld starts here because an unreadable field list is already a reason
	// to withhold: the set holds the WHOLE auth file, so a log line printing
	// only the access token is not a substring of anything in it. That is an
	// incomplete scan, not a less precise one, and adoption refuses to publish
	// on exactly this condition (adoptionSensitive) — the two paths may not
	// disagree about what counts as a complete scan.
	withheld := false
	if spec.Custody == custodyMaterialized && d.sensitive != nil {
		sens, err := d.sensitive(g.secret)
		if err != nil {
			r.log.Warn("runner.sensitive_fields", "attempt", ref.String(), "error", err)
			withheld = true
		}
		set.add(sens...)
	}

	// Every credential-bearing attempt gets a manifest, whatever its custody:
	// adoption rebuilds the redaction set from it, and an attempt whose
	// manifest is missing has to have its logs and artifacts withheld.
	built, err := r.manifestFor(req, spec, d, g)
	if err != nil {
		// Bytes that cannot be identity-pinned are dead for every phase bound
		// to this credential, not only for this attempt, so the freshness
		// signal is stamped here exactly as it is for a provider's own
		// rejection at exit: nothing else fails this way twice.
		if runs.ClassifyOutcome(err).Code == runs.CodeAuthExpired {
			r.markAuthExpired(ctx, ref, req.Credential.ID)
		}
		return runs.ExecutionResult{}, err
	}
	// Assigned only on success: the zero manifest an error rides back with
	// would erase the epoch the deferred conclusion releases at.
	m = built
	if req.Credential.ID != uuid.Nil {
		if err := r.writeManifest(ref, m); err != nil {
			return runs.ExecutionResult{}, transientf(runs.CodeInternalError, "write attempt manifest: %v", err)
		}
		// A file-delivered credential lands on the host before any container
		// exists: the bind is resolved when the agent is created, and by then
		// the manifest describing the directory is already durable. Both live
		// under the one guard, so a secret can never reach the disk with
		// nothing on record to say it is there.
		if d.materializesAuthFile(spec.Custody) {
			if err := r.materializeAuthDir(ref, d, g.secret); err != nil {
				return runs.ExecutionResult{}, transientf(runs.CodeInternalError, "materialize auth dir: %v", err)
			}
			spec.AuthBind = r.hostPath(r.authDir(ref)) + ":" + d.authMount.containerPath
		}
	}
	// The materialized half of the agent's environment is completed here and
	// only here: it carries the secret, which buildSpec has no business
	// reading. A brokered attempt's env was finished before the credential
	// was ever opened.
	if spec.Custody == custodyMaterialized {
		spec.Env = append(spec.Env, d.env(envInput{
			custody: custodyMaterialized, secret: spec.ProviderCredential,
		})...)
	}

	clone, err := r.git.CloneSpec(ctx, req.Repo)
	if err != nil {
		return runs.ExecutionResult{}, transientf(runs.CodeStagingFailed, "resolve clone for %s: %v", req.Repo.Slug, err)
	}

	if err := r.ensureNetworks(ctx, ref); err != nil {
		return runs.ExecutionResult{}, err
	}
	if err := r.stageAttempt(ctx, ref, spec, clone); err != nil {
		return runs.ExecutionResult{}, err
	}
	agent, err := r.runAgentToCompletion(ctx, ref, spec, d)
	// The credential obligation settles before any result is assembled: a
	// success may not be reported while its sync is pending (the receipt
	// precedes the terminal result), and what the read-back found outranks
	// whatever the agent exited with — quarantine first, then an unresolved
	// sync, then the agent's own outcome.
	//
	// The conclusion is also what completes the redaction set, so the order is
	// pinned: conclude, extend the set, publish or withhold, and only then
	// assemble anything an artifact could carry (spec §3).
	if leased {
		// The heartbeat stops (and its goroutine joins) before the conclusion
		// releases: a tick landing after the release reads as a fence at the
		// aggregate, and the resulting cancel would fail the result collection
		// of an attempt that had already synced and released cleanly.
		stopHeartbeat()
		conc := conclude()
		set.add(conc.RefreshedSensitive...)
		// A read-back nobody could parse means the refreshed value is
		// unknowable, so no complete scan exists and nothing this attempt
		// produced may publish (spec §2.7). The streams stay in quarantine as
		// evidence; only the typed outcome still gets out.
		withheld = withheld || conc.Status == concUnscannable
		r.publishOrWithhold(ref, set, withheld)
		switch {
		case conc.Outcome != nil:
			return r.failureResult(ref, agent, set, withheld), runs.OutcomeError{Outcome: *conc.Outcome}
		case conc.Status == concFenced:
			// The lease stays held; the stale-lease janitor settles it, and
			// the retried attempt re-acquires idempotently. Nothing charged.
			return r.failureResult(ref, agent, set, withheld), transientf(runs.CodeCredentialBusy,
				"credential %s: sync-back unresolved, custody belongs to reconciliation", req.Credential.ID)
		}
	} else {
		// Nothing about an unleased credential can change while the attempt
		// runs: the set was as complete when the agent started as it will ever
		// be, and whether that is complete enough was already decided above.
		r.publishOrWithhold(ref, set, withheld)
	}
	if err != nil {
		return r.failureResult(ref, agent, set, withheld), err
	}
	res, err := r.collectResult(ctx, collection{
		Ref: ref, BaseSHA: spec.BaseSHA, User: spec.User,
		Kind: spec.Harness.Kind, CredentialID: req.Credential.ID,
		ExitCode: agent.exitCode, OOM: agent.oom,
		Sensitive: set, Unscannable: withheld,
	})
	if err != nil {
		return r.failureResult(ref, agent, set, withheld), err
	}
	return res, nil
}

// publishOrWithhold moves the attempt's quarantined streams into the published
// logs directory, unless this attempt is withholding them.
//
// A publication that fails is logged and survived: the bytes stay quarantined,
// so nothing unscanned is exposed, and an attempt's real result must not turn
// on a copy that hit a full disk. The log_ref it leaves behind reads the sizes
// off disk and reports what actually landed.
func (r *Runner) publishOrWithhold(ref runs.AttemptRef, set *sensitiveSet, withheld bool) {
	if withheld {
		return
	}
	if err := r.publishLogs(ref, set); err != nil {
		r.log.Warn("runner.publish_logs", "attempt", ref.String(), "error", err)
	}
}

// agentOutcome is what the untrusted half of the pipeline left behind: the
// agent's corpse, inspected before teardown removed it.
type agentOutcome struct {
	started  bool // the agent container actually ran
	inspect  dockerapi.ContainerInspect
	exitCode int64
	oom      bool
}

// failureResult assembles the evidence a failed attempt owes the
// orchestrator, which persists artifacts that ride a failure return. A
// failure before the agent ever started carries none: nothing ran, so there
// is nothing to describe, and an empty snapshot would only look like one.
//
// A withheld attempt owes the container metadata and nothing else. The log
// tail and the workspace listing are both agent-chosen bytes — snapshot.go
// lists filenames, and a file named after the refreshed token would ride the
// snapshot into the store — and withheld means precisely that nobody holds the
// value to redact them against (spec §2.7). The log pointer goes too: there is
// nothing published for it to point at.
func (r *Runner) failureResult(ref runs.AttemptRef, agent agentOutcome, set *sensitiveSet, withheld bool) runs.ExecutionResult {
	if !agent.started {
		return runs.ExecutionResult{}
	}
	if set == nil {
		// Unreachable by construction, and fails closed the way publishLogs
		// does: no set means nothing scanned anything, which is what withheld
		// means. The empty stand-in keeps the metadata redaction total.
		set, withheld = newSensitiveSet(), true
	}
	if withheld {
		return runs.ExecutionResult{Artifacts: []runs.NewArtifact{{
			Type:    runs.ArtifactKindFailureSnapshot,
			Payload: containerMetadata(agent.inspect, set.strings()),
		}}}
	}
	return runs.ExecutionResult{Artifacts: []runs.NewArtifact{
		r.logRefArtifact(ref),
		{
			Type: runs.ArtifactKindFailureSnapshot,
			Payload: buildFailureSnapshot(agent.inspect,
				filepath.Join(r.logsDir(ref), agentLogFile), snapshotTailBytes,
				filepath.Join(r.attemptDir(ref), gitops.WorkspaceDir),
				set.strings()),
		},
	}}
}

// ensureNetworks brings up the shared egress network (once per host) and this
// attempt's own internal network.
func (r *Runner) ensureNetworks(ctx context.Context, ref runs.AttemptRef) error {
	if err := r.ensureEgressNetwork(ctx); err != nil {
		return err
	}
	// internal=true is the isolation: nothing on this network has a route
	// off the host except through the dual-homed gateway.
	if _, err := r.engine.NetworkCreate(ctx, networkName(ref), true, labels(ref)); err != nil {
		return transientf(runs.CodeInternalError, "create network %s: %v", networkName(ref), err)
	}
	return nil
}

// ensureEgressNetwork brings up the one network with a route off the host.
// It is created on demand and raced against every other attempt — and every
// validation run — starting at the same moment, so an already-exists answer
// is success.
func (r *Runner) ensureEgressNetwork(ctx context.Context) error {
	if _, err := r.engine.NetworkInspect(ctx, r.opts.EgressNetwork); err == nil {
		return nil
	} else if !dockerapi.IsNotFound(err) {
		return transientf(runs.CodeInternalError, "inspect network %s: %v", r.opts.EgressNetwork, err)
	}
	_, err := r.engine.NetworkCreate(ctx, r.opts.EgressNetwork, false, map[string]string{"caesar.managed": "true"})
	if err != nil && !dockerapi.IsConflict(err) {
		return transientf(runs.CodeInternalError, "create network %s: %v", r.opts.EgressNetwork, err)
	}
	return nil
}

// stageAttempt runs the trusted stage container: it clones at the pinned SHA,
// sanitizes and verifies, and reports through a contract file. The report is
// the signal, never the exit code — a container that died before writing one
// staged nothing.
func (r *Runner) stageAttempt(ctx context.Context, ref runs.AttemptRef, spec RunSpec, clone CloneSpec) error {
	dir := r.attemptDir(ref)
	in := gitops.StageInput{
		CloneURL:   clone.URL,
		BaseSHA:    spec.BaseSHA,
		QuotaBytes: r.opts.WorkspaceQuotaBytes,
		QuotaFiles: r.opts.WorkspaceQuotaFiles,
	}
	if err := writeContract(filepath.Join(dir, gitops.StageInputFile), in); err != nil {
		return transientf(runs.CodeStagingFailed, "write stage contract: %v", err)
	}

	binds := []string{r.hostPath(dir) + ":" + gitops.AttemptMount}
	network := r.opts.EgressNetwork
	if clone.BindDir != "" {
		// A fixture remote is mounted read-only and needs no network at all.
		binds = append(binds, clone.BindDir+":"+remoteMount+":ro")
		network = noNetwork
	}
	// The forge credential exists only as an http.extraheader in this
	// process env — never in the URL, never in a file inside the attempt.
	var env []string
	if clone.AuthHeader != "" {
		env = []string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraheader",
			"GIT_CONFIG_VALUE_0=" + clone.AuthHeader,
		}
	}

	code, runErr := r.runToCompletion(ctx, stageName(ref), dockerapi.ContainerConfig{
		Image:  r.opts.GitImage,
		Cmd:    []string{"stage"},
		Env:    env,
		User:   spec.User,
		Labels: labels(ref),
	}, r.hardenedHost(binds, network, gitMemoryBytes, gitNanoCPUs, gitPidsLimit), nil, stageTimeout, nil)

	rep, readErr := readContract[gitops.StageReport](filepath.Join(dir, gitops.StageReportFile), stageReportLimit)
	if readErr != nil {
		if runErr != nil {
			return transientf(runs.CodeStagingFailed, "stage container: %v", runErr)
		}
		return transientf(runs.CodeStagingFailed, "stage container exited %d without a readable report: %v", code, readErr)
	}
	if rep.Outcome != nil {
		return runs.OutcomeError{Outcome: *rep.Outcome}
	}
	if !rep.Verified {
		return transientf(runs.CodeStagingFailed, "stage container exited %d without verifying the workspace", code)
	}
	r.log.Info("runner.staged", "attempt", ref.String(), "head", rep.HeadSHA, "files", rep.Files, "bytes", rep.Bytes)
	return nil
}

// runAgentToCompletion owns the untrusted half of the pipeline: the gateway
// comes up first so the agent never races a missing broker, both containers
// stream into capped log files under quarantine, and the gateway is stopped
// gracefully at the end so its usage summary flushes.
//
// Both streams are quarantined, the gateway's included: it should hold no
// secret of its own, but the broker token transits it, and "should" is not a
// property worth publishing unscanned bytes for.
func (r *Runner) runAgentToCompletion(ctx context.Context, ref runs.AttemptRef, spec RunSpec, d driver) (agentOutcome, error) {
	var out agentOutcome

	// The log streams deliberately do not inherit the attempt's deadline: a
	// killed agent's last words are exactly what an operator needs. They end
	// when their container does, or when this function returns.
	streamCtx, stopStreams := context.WithCancel(context.WithoutCancel(ctx))
	var streams []*logStreamer
	defer func() { r.drainStreams(ref, streams, stopStreams) }()

	gatewayID, err := r.startGateway(ctx, ref, spec, d)
	if err != nil {
		return out, err
	}
	// The gateway's stream carries no flood kill: it is trusted code whose
	// volume is one line per request, it is already bounded by the per-stream
	// cap, and it runs before there is an agent to kill.
	streams = append(streams, r.streamLogs(streamCtx, gatewayID,
		filepath.Join(r.quarantineDir(ref), gatewayLogFile), ref, streamOpts{
			stream:   "gateway",
			onDenied: r.denialReporter(ctx, ref),
		}))

	agentID, err := r.startAgent(ctx, ref, spec)
	if err != nil {
		return out, err
	}
	out.started = true

	// execCtx records why the agent was killed. A killed container exits 137
	// whatever the reason, so the cause — not the exit code — is what says
	// which limit it hit. The first cause wins, so an attempt that breaches
	// two limits at once is still reported as one death.
	execCtx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(context.Canceled)
	enforce := func(reason error) {
		cancelCause(reason)
		r.log.Warn("runner.enforced", "attempt", ref.String(), "reason", reason.Error())
		if err := r.engine.ContainerKill(context.WithoutCancel(ctx), agentID, "KILL"); err != nil &&
			!dockerapi.IsNotFound(err) && !dockerapi.IsConflict(err) {
			r.log.Warn("runner.agent_kill", "attempt", ref.String(), "error", err)
		}
	}

	streams = append(streams, r.streamLogs(streamCtx, agentID,
		filepath.Join(r.quarantineDir(ref), agentLogFile), ref, streamOpts{
			stream: "agent",
			onFlood: func() {
				enforce(permanentf(runs.CodeLogFlood,
					"agent produced more than %d bytes of log output", r.opts.LogFloodBytes))
			},
		}))

	watchCtx, stopWatch := context.WithCancel(streamCtx)
	defer stopWatch()
	go watchWorkspace(watchCtx, filepath.Join(r.attemptDir(ref), gitops.WorkspaceDir),
		r.opts.WorkspaceQuotaBytes, r.opts.WorkspaceQuotaFiles, r.opts.QuotaPollInterval,
		func(st gitops.WalkStats) {
			enforce(permanentf(runs.CodeWorkspaceQuotaExceeded,
				"workspace exceeded its quota: %d files, %d bytes past %d files / %d bytes",
				st.Files, st.Bytes, r.opts.WorkspaceQuotaFiles, r.opts.WorkspaceQuotaBytes))
		})

	code, waitErr := r.engine.ContainerWait(ctx, agentID)
	stopWatch()

	// A dead context means the attempt's deadline passed; the agent has no
	// say in that, so it is killed rather than asked.
	expired := ctx.Err() != nil
	if expired {
		if err := r.engine.ContainerKill(context.WithoutCancel(ctx), agentID, "KILL"); err != nil && !dockerapi.IsNotFound(err) && !dockerapi.IsConflict(err) {
			r.log.Warn("runner.agent_kill", "attempt", ref.String(), "error", err)
		}
		// The wait above returned because the deadline passed, not because
		// the agent stopped; the snapshot below deserves a real corpse.
		reapCtx, cancelReap := context.WithTimeout(context.WithoutCancel(ctx), agentCorpseGrace)
		if reaped, err := r.engine.ContainerWait(reapCtx, agentID); err == nil {
			out.exitCode = reaped
		}
		cancelReap()
	}

	// The corpse is inspected before the gateway is stopped and long before
	// teardown: after that there is nothing left to ask.
	if !expired {
		out.exitCode = code
	}
	if insp, err := r.engine.ContainerInspect(context.WithoutCancel(ctx), agentID); err == nil {
		out.inspect, out.oom = insp, insp.State.OOMKilled
	} else {
		r.log.Warn("runner.agent_inspect", "attempt", ref.String(), "error", err)
	}
	r.stopGateway(ctx, gatewayID)

	// Enforcement speaks first: an agent this runner killed did not fail on
	// its own terms, and its exit code is an artifact of the signal.
	var enforced runs.OutcomeError
	if errors.As(context.Cause(execCtx), &enforced) {
		return out, enforced
	}
	if expired {
		return out, ctx.Err()
	}
	if waitErr != nil {
		return out, transientf(runs.CodeHarnessError, "wait for agent: %v", waitErr)
	}
	return out, nil
}

// startGateway brings up the attempt's egress process: dual-homed onto the
// attempt's internal network (where it answers to the name "egress") and the
// shared egress network (where it is the only container with a route out).
// Under brokered custody the provider credential exists in exactly one place —
// this container's environment — and the agent's network cannot reach the
// daemon that holds it; under materialized custody the gateway holds no
// credential at all and is purely the allowlisted route out.
func (r *Runner) startGateway(ctx context.Context, ref runs.AttemptRef, spec RunSpec, d driver) (string, error) {
	raw, err := json.Marshal(r.gatewayConfig(spec, d))
	if err != nil {
		return "", transientf(runs.CodeInternalError, "encode egress config: %v", err)
	}

	host := r.hardenedHost(nil, networkName(ref), gatewayMemoryBytes, gatewayNanoCPUs, gatewayPidsLimit)
	if r.opts.testHostAccess {
		host.ExtraHosts = []string{"host.docker.internal:host-gateway"}
	}
	id, err := r.engine.ContainerCreate(ctx, gatewayName(ref), dockerapi.ContainerConfig{
		Image:  r.opts.CaesarImage,
		Cmd:    []string{"egress-proxy"},
		Env:    []string{config.EgressConfigEnv + "=" + string(raw)},
		User:   spec.User,
		Labels: labels(ref),
	}, host, map[string]dockerapi.EndpointSettings{networkName(ref): {Aliases: []string{gatewayAlias}}})
	if err != nil {
		return "", transientf(runs.CodeInternalError, "create gateway: %v", err)
	}
	// The daemon takes one endpoint per create at this API version, so the
	// second home is joined before the process ever runs.
	if err := r.engine.NetworkConnect(ctx, r.opts.EgressNetwork, id, nil); err != nil {
		return "", transientf(runs.CodeInternalError, "attach gateway to %s: %v", r.opts.EgressNetwork, err)
	}
	if err := r.engine.ContainerStart(ctx, id); err != nil {
		return "", transientf(runs.CodeInternalError, "start gateway: %v", err)
	}
	return id, nil
}

// startAgent runs the harness. Its whole world is here: the pinned image, the
// constructed environment, the workspace, and one internal network whose only
// other occupant is the gateway.
func (r *Runner) startAgent(ctx context.Context, ref runs.AttemptRef, spec RunSpec) (string, error) {
	ws := filepath.Join(r.attemptDir(ref), gitops.WorkspaceDir)
	// The auth directory rides the same mechanism as the workspace and is the
	// only other path the agent shares with the host. It is writable on
	// purpose: the CLI refreshes its own tokens, and that refreshed file is
	// what sync-back reads back (spec §2.4).
	binds := []string{r.hostPath(ws) + ":" + workspaceDir}
	if spec.AuthBind != "" {
		binds = append(binds, spec.AuthBind)
	}
	host := r.hardenedHost(binds, networkName(ref), r.opts.MemoryBytes, r.opts.NanoCPUs, r.opts.PidsLimit)

	id, err := r.engine.ContainerCreate(ctx, agentName(ref), dockerapi.ContainerConfig{
		Image:      spec.ImageDigest,
		Entrypoint: spec.Harness.Entrypoint,
		Cmd:        spec.Harness.Cmd,
		Env:        spec.Env,
		User:       spec.User,
		WorkingDir: workspaceDir,
		Labels:     labels(ref),
	}, host, map[string]dockerapi.EndpointSettings{networkName(ref): {}})
	if err != nil {
		return "", transientf(runs.CodeInternalError, "create agent: %v", err)
	}
	if err := r.engine.ContainerStart(ctx, id); err != nil {
		return "", transientf(runs.CodeInternalError, "start agent: %v", err)
	}
	return id, nil
}

// stopGateway asks the gateway to finish so its final egress.summary line —
// the attempt's usage bill — reaches the log, and kills it if it will not.
func (r *Runner) stopGateway(ctx context.Context, id string) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayStopGrace)
	defer cancel()
	if err := r.engine.ContainerKill(stopCtx, id, "TERM"); err != nil {
		if !dockerapi.IsNotFound(err) && !dockerapi.IsConflict(err) { // conflict = already stopped
			r.log.Warn("runner.gateway_stop", "container", id, "error", err)
		}
		return
	}
	if _, err := r.engine.ContainerWait(stopCtx, id); err != nil {
		killCtx, killCancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayStopGrace)
		defer killCancel()
		if err := r.engine.ContainerKill(killCtx, id, "KILL"); err != nil && !dockerapi.IsNotFound(err) && !dockerapi.IsConflict(err) {
			r.log.Warn("runner.gateway_kill", "container", id, "error", err)
		}
	}
}

// denialReporter turns the gateway's refusals into run events, capped. The
// counter needs no lock: one log streamer calls this, from one goroutine.
//
// The gateway logs its refusal category as `kind`; the event payload names it
// `reason`, which is the pinned key consumers read. The rename happens here,
// at the one boundary between the two vocabularies.
func (r *Runner) denialReporter(ctx context.Context, ref runs.AttemptRef) func(host, kind string) {
	seen := 0
	return func(host, kind string) {
		seen++
		if seen > maxDenialEvents || r.events == nil {
			return
		}
		err := r.events.AppendRuntimeEvent(context.WithoutCancel(ctx), ref, "phase.egress_denied", map[string]any{
			"host":   host,
			"reason": kind,
		})
		if err != nil {
			r.log.Warn("runner.egress_denied.event", "attempt", ref.String(), "error", err)
		}
	}
}

// drainStreams ends the log follows one container set produced. Each stream
// gets a moment to end by itself first: cancelling a log follow aborts the
// HTTP read, which discards whatever the daemon had already flushed — and the
// last of those bytes is the gateway's egress.summary, the very line the TERM
// grace exists to produce. The cancel stays as the escape hatch for error
// paths, where a container may still be running and nothing will end the
// stream on its own. Calling it twice is safe: the second pass finds every
// stream already done.
func (r *Runner) drainStreams(ref runs.AttemptRef, streams []*logStreamer, stop context.CancelFunc) {
	for _, s := range streams {
		select {
		case <-s.done:
		case <-time.After(streamDrainGrace):
			stop()
		}
	}
	stop()
	for _, s := range streams {
		if _, _, err := s.wait(); err != nil {
			r.log.Warn("runner.log_stream", "attempt", ref.String(), "error", err)
		}
	}
}

// teardown removes everything one attempt created, tolerating anything that
// is already gone. It runs on every return path, including the panicking one,
// and the logs directory deliberately survives it: logs are evidence.
func (r *Runner) teardown(ctx context.Context, ref runs.AttemptRef) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownTimeout)
	defer cancel()

	r.removeByName(ctx,
		[]string{extractName(ref), agentName(ref), gatewayName(ref), stageName(ref)},
		[]string{networkName(ref)})
	if err := os.RemoveAll(r.attemptDir(ref)); err != nil {
		r.log.Warn("runner.teardown.attempt_dir", "attempt", ref.String(), "error", err)
	}
}

func transientf(code, format string, args ...any) error {
	return runs.OutcomeError{Outcome: runs.Outcome{
		Class: runs.OutcomeTransient, Code: code, Message: fmt.Sprintf(format, args...),
	}}
}

func permanentf(code, format string, args ...any) error {
	return runs.OutcomeError{Outcome: runs.Outcome{
		Class: runs.OutcomePermanent, Code: code, Message: fmt.Sprintf(format, args...),
	}}
}
