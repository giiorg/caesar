package runner

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func testRef() runs.AttemptRef {
	return runs.AttemptRef{
		RunID:   uuid.MustParse("018f0000-0000-7000-8000-000000000001"),
		NodeID:  "implement",
		Attempt: 2,
	}
}

func TestNaming_AllDerivedFromAttemptRef(t *testing.T) {
	ref := testRef()
	base := ref.String()
	cases := []struct {
		resource string
		got      string
		want     string
	}{
		{"agent container", agentName(ref), base},
		{"gateway container", gatewayName(ref), base + "-egress"},
		{"stage container", stageName(ref), base + "-stage"},
		{"extract container", extractName(ref), base + "-extract"},
		{"validation container", validateName(ref), base + "-validate"},
		{"attempt network", networkName(ref), base + "-net"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: name = %q, want %q", c.resource, c.got, c.want)
		}
	}
}

func TestLabels_MarkManagedAndCarryAttemptIdentity(t *testing.T) {
	ref := testRef()
	want := map[string]string{
		"caesar.managed": "true",
		"caesar.run":     ref.RunID.String(),
		"caesar.node":    "implement",
		"caesar.attempt": "2",
	}
	if diff := cmp.Diff(want, labels(ref)); diff != "" {
		t.Errorf("labels mismatch (-want +got):\n%s", diff)
	}
}

func TestAttemptAndLogDirs_LiveUnderDataRoot(t *testing.T) {
	r := &Runner{opts: Options{DataRoot: "/var/lib/caesar"}}
	ref := testRef()
	if got, want := r.attemptDir(ref), "/var/lib/caesar/attempts/"+ref.String(); got != want {
		t.Errorf("attemptDir = %q, want %q", got, want)
	}
	if got, want := r.logsDir(ref), "/var/lib/caesar/logs/"+ref.String(); got != want {
		t.Errorf("logsDir = %q, want %q", got, want)
	}
}

func TestHostPath_MapsDataRootPrefix(t *testing.T) {
	r := &Runner{opts: Options{DataRoot: "/var/lib/caesar", DataHostRoot: "/srv/caesar-data"}}
	if got := r.hostPath("/var/lib/caesar/attempts/x/workspace"); got != "/srv/caesar-data/attempts/x/workspace" {
		t.Fatalf("hostPath = %s", got)
	}
}

func TestHostPath_LeavesForeignPathsAlone(t *testing.T) {
	cases := []struct {
		name     string
		dataRoot string
		hostRoot string
		in       string
		want     string
	}{
		{"outside the data root", "/var/lib/caesar", "/srv/caesar-data", "/etc/passwd", "/etc/passwd"},
		{"prefix is a sibling, not a parent", "/var/lib/caesar", "/srv/caesar-data", "/var/lib/caesar-other/x", "/var/lib/caesar-other/x"},
		{"host root equals data root", "/var/lib/caesar", "/var/lib/caesar", "/var/lib/caesar/logs/x", "/var/lib/caesar/logs/x"},
		{"the data root itself", "/var/lib/caesar", "/srv/caesar-data", "/var/lib/caesar", "/srv/caesar-data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Runner{opts: Options{DataRoot: c.dataRoot, DataHostRoot: c.hostRoot}}
			if got := r.hostPath(c.in); got != c.want {
				t.Errorf("hostPath(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
