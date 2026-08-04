package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
)

// Per-role ceilings. Only the agent's come from Options: the trusted
// containers are ours and their work is bounded, so handing them the agent's
// budget would let a hostile workspace turn staging or extraction into a
// resource event of its own.
const (
	gatewayMemoryBytes int64 = 256 << 20
	gatewayNanoCPUs    int64 = 1_000_000_000
	gatewayPidsLimit   int64 = 128

	gitMemoryBytes int64 = 1 << 30
	gitNanoCPUs    int64 = 1_000_000_000
	gitPidsLimit   int64 = 256

	// tmpfsBytes sizes the one writable path on an otherwise read-only
	// rootfs. It is RAM, so it counts against the container's memory.
	tmpfsBytes = 256 << 20

	// noNetwork is Docker's spelling of "no interfaces but loopback".
	noNetwork = "none"

	containerStopTimeout = 30 * time.Second
)

// hardenedHost is the posture every container in the pipeline runs under,
// with only the ceilings varying by role: no capabilities, no way to gain
// privileges through setuid, an immutable root filesystem, and one sized
// tmpfs for the scratch space a real program still needs.
//
// Restart policy and auto-remove are deliberately not sent: the daemon's
// defaults are already "never restart" and "keep the container until it is
// removed", and teardown is explicit here precisely so a corpse can be
// inspected before it is swept.
func (r *Runner) hardenedHost(binds []string, network string, mem, cpus, pids int64) dockerapi.HostConfig {
	return dockerapi.HostConfig{
		Binds:          binds,
		NetworkMode:    network,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges=true"},
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": fmt.Sprintf("rw,size=%d", tmpfsBytes)},
		Memory:         mem,
		NanoCpus:       cpus,
		PidsLimit:      &pids,
	}
}

// runToCompletion creates, starts and waits out one short-lived container,
// removing it on every path — including the paths where the wait itself
// failed, because a container nobody is waiting for is an orphan. It serves
// the trusted containers (stage, extract, validation); the agent and the
// gateway keep explicit lifecycles in execute.go, where the log streaming and
// the kill windows differ.
//
// started, when non-nil, is called with the container's id the moment it is
// running and before the wait — the only window in which a caller can follow
// its logs, since the container is gone by the time this returns.
func (r *Runner) runToCompletion(ctx context.Context, name string, cfg dockerapi.ContainerConfig,
	host dockerapi.HostConfig, networks map[string]dockerapi.EndpointSettings, timeout time.Duration,
	started func(id string),
) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	id, err := r.engine.ContainerCreate(ctx, name, cfg, host, networks)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", name, err)
	}
	defer r.removeContainer(ctx, id)

	if err := r.engine.ContainerStart(ctx, id); err != nil {
		return 0, fmt.Errorf("start %s: %w", name, err)
	}
	if started != nil {
		started(id)
	}
	code, err := r.engine.ContainerWait(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("wait %s: %w", name, err)
	}
	return code, nil
}

// removeContainer forces one container away with a fresh deadline: the
// context that ran it may be the one that just expired, and the removal must
// happen anyway.
func (r *Runner) removeContainer(ctx context.Context, id string) {
	rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerStopTimeout)
	defer cancel()
	if err := r.engine.ContainerRemove(rmCtx, id, true); err != nil && !dockerapi.IsNotFound(err) {
		r.log.Warn("runner.container_remove", "container", id, "error", err)
	}
}
