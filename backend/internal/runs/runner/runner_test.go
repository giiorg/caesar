package runner

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func discardLog() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func newRunner(t *testing.T, opts Options) (*Runner, error) {
	t.Helper()
	return New(&fakeEngine{}, nil, nil, nil, nil, opts, discardLog())
}

// The runner is also a runs.Admission: the spine gates claiming on it.
var _ runs.Admission = (*Runner)(nil)

func TestNew_RejectsAnUnusableConfiguration(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"no caesar image", Options{GitImage: "caesar-git:dev"}},
		{"no git image", Options{CaesarImage: "caesar:dev"}},
		{"flood threshold below the log cap", Options{
			CaesarImage: "caesar:dev", GitImage: "caesar-git:dev",
			LogCapBytes: 64 << 20, LogFloodBytes: 16 << 20,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := newRunner(t, c.opts); err == nil {
				t.Fatal("want New to refuse")
			}
		})
	}
}

func TestNew_FillsEveryZeroKnobWithItsPinnedDefault(t *testing.T) {
	r, err := newRunner(t, Options{CaesarImage: "caesar:dev", GitImage: "caesar-git:dev"})
	if err != nil {
		t.Fatal(err)
	}
	want := Options{
		DataRoot: "/var/lib/caesar", DataHostRoot: "/var/lib/caesar",
		CaesarImage: "caesar:dev", GitImage: "caesar-git:dev",
		EgressNetwork:       "caesar-egress",
		WorkspaceQuotaBytes: 2 << 30, WorkspaceQuotaFiles: 200_000,
		LogCapBytes: 16 << 20, LogFloodBytes: 64 << 20,
		FreeBytesWatermark: 10 << 30, MemoryBytes: 4 << 30,
		NanoCPUs: 2_000_000_000, PidsLimit: 512,
		QuotaPollInterval: 2 * time.Second,
	}
	if diff := cmp.Diff(want, r.opts); diff != "" {
		t.Errorf("defaults mismatch (-want +got):\n%s", diff)
	}
	if r.uid != os.Getuid() || r.gid != os.Getgid() {
		t.Errorf("uid:gid = %d:%d, want the control plane's own %d:%d", r.uid, r.gid, os.Getuid(), os.Getgid())
	}
}

func TestNew_KeepsExplicitOptions(t *testing.T) {
	r, err := newRunner(t, Options{
		CaesarImage: "caesar:dev", GitImage: "caesar-git:dev",
		DataRoot: "/data", DataHostRoot: "/host/data",
		FreeBytesWatermark: 1 << 30, ConnectAllow: []string{"registry.npmjs.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.opts.DataHostRoot != "/host/data" || r.opts.FreeBytesWatermark != 1<<30 {
		t.Errorf("explicit options were overwritten: %+v", r.opts)
	}
	if diff := cmp.Diff([]string{"registry.npmjs.org"}, r.opts.ConnectAllow); diff != "" {
		t.Errorf("connect allowlist mismatch (-want +got):\n%s", diff)
	}
}

func TestNew_RefusesToStartAsRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("the uid-0 guard is only observable when the test process is root")
	}
	if _, err := newRunner(t, Options{CaesarImage: "caesar:dev", GitImage: "caesar-git:dev"}); err == nil {
		t.Fatal("attempt containers run as this process's uid:gid, so root must be refused")
	}
}
