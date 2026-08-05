package runner

import (
	"errors"
	"io"
	"log/slog"
	"testing"
)

// statfs is injectable so the watermark logic is unit-testable without a
// real filesystem near its free-space limit.
func TestAdmitClaim_RefusesUnderWatermark(t *testing.T) {
	r := &Runner{opts: Options{FreeBytesWatermark: 10 << 30, DataRoot: "/x"}, log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	r.freeBytes = func(string) (int64, error) { return 5 << 30, nil }
	if err := r.AdmitClaim(t.Context()); err == nil {
		t.Fatal("want refusal under watermark")
	}
	r.freeBytes = func(string) (int64, error) { return 50 << 30, nil }
	if err := r.AdmitClaim(t.Context()); err != nil {
		t.Fatalf("want admit above watermark: %v", err)
	}
}

func TestAdmitClaim_StatfsFailureRefusesClaim(t *testing.T) {
	boom := errors.New("statfs exploded")
	r := &Runner{opts: Options{FreeBytesWatermark: 10 << 30, DataRoot: "/x"}, log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	r.freeBytes = func(string) (int64, error) { return 0, boom }
	err := r.AdmitClaim(t.Context())
	if err == nil {
		t.Fatal("want refusal when free space is unknowable")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the statfs failure", err)
	}
}
