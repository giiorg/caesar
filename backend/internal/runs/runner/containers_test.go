package runner

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
)

// TestHardenedHost_PinsTheContainerHardeningProfile fixes the whole hardening
// posture at the source, so a dropped flag fails here rather than only under a
// real daemon. The isolation suite's in-container probes cannot carry this on
// their own: an agent runs as a non-root uid, so `touch /` fails on directory
// permissions whether or not ReadonlyRootfs is set — that probe would stay
// green with the flag removed. This asserts the flag itself.
//
// hardenedHost reads only its arguments, never a Runner field, so the zero
// receiver is enough to exercise it.
func TestHardenedHost_PinsTheContainerHardeningProfile(t *testing.T) {
	binds := []string{"/host/ws:/workspace"}
	const (
		network = "attempt-net"
		mem     = int64(4 << 30)
		cpus    = int64(2_000_000_000)
		pids    = int64(512)
	)

	got := (&Runner{}).hardenedHost(binds, network, mem, cpus, pids)

	wantPids := pids
	want := dockerapi.HostConfig{
		Binds:          binds,
		NetworkMode:    network,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges=true"},
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": "rw,size=268435456"}, // rw,size=<tmpfsBytes>
		Memory:         mem,
		NanoCpus:       cpus,
		PidsLimit:      &wantPids,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("hardenedHost profile (-want +got):\n%s", diff)
	}
}
