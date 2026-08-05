package runner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

func TestWatchLimits_NeverHandsTheWalkANonPositiveCap(t *testing.T) {
	// gitops.BoundedWalk reads a non-positive cap as "no cap at all", so a
	// zero arriving here would turn the watchdog into a meter that never
	// fires. Every non-positive input becomes the pinned default instead.
	tests := []struct {
		name                 string
		bytes, files         int64
		poll                 time.Duration
		wantBytes, wantFiles int64
		wantPoll             time.Duration
	}{
		{
			name:  "configured limits pass through",
			bytes: 5, files: 7, poll: time.Second,
			wantBytes: 5, wantFiles: 7, wantPoll: time.Second,
		},
		{
			name:      "zeroes become the pinned defaults",
			wantBytes: defaultWorkspaceQuotaBytes, wantFiles: defaultWorkspaceQuotaFiles,
			wantPoll: defaultQuotaPollInterval,
		},
		{
			name:  "negatives become the pinned defaults",
			bytes: -1, files: -1, poll: -time.Second,
			wantBytes: defaultWorkspaceQuotaBytes, wantFiles: defaultWorkspaceQuotaFiles,
			wantPoll: defaultQuotaPollInterval,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotBytes, gotFiles, gotPoll := watchLimits(tc.bytes, tc.files, tc.poll)
			if gotBytes != tc.wantBytes || gotFiles != tc.wantFiles || gotPoll != tc.wantPoll {
				t.Errorf("watchLimits = (%d, %d, %v), want (%d, %d, %v)",
					gotBytes, gotFiles, gotPoll, tc.wantBytes, tc.wantFiles, tc.wantPoll)
			}
		})
	}
}

func TestWatchWorkspace_BreachesOnceThenStops(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(dir, string(rune('a'+i))), []byte("0123456789"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	var calls []gitops.WalkStats
	fired := make(chan struct{})
	breach := func(st gitops.WalkStats) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, st)
		if len(calls) == 1 {
			close(fired)
		}
	}

	go watchWorkspace(t.Context(), dir, 5, 100, time.Millisecond, breach)
	select {
	case <-fired:
	case <-time.After(10 * time.Second):
		t.Fatal("a workspace over its byte quota never breached")
	}

	// The watchdog returns after the breach: whatever the agent writes next,
	// the attempt is already dying and one kill is all it gets.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("breach fired %d times, want exactly once", len(calls))
	}
	if !calls[0].Breached || calls[0].Bytes < 5 {
		t.Errorf("breach stats = %+v, want the reading that tripped it", calls[0])
	}
}

func TestWatchWorkspace_QuietUnderQuotaAndStopsWithTheContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	breached := false
	go func() {
		defer close(done)
		watchWorkspace(ctx, dir, 1<<20, 1000, time.Millisecond, func(gitops.WalkStats) { breached = true })
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the watchdog outlived its context")
	}
	if breached {
		t.Error("a workspace inside its quota breached")
	}
}

func TestWatchWorkspace_TolerateAVanishedWorkspace(t *testing.T) {
	// Teardown races the watchdog on every attempt: the directory can go
	// while a walk is in flight, and that is not a quota breach.
	dir := filepath.Join(t.TempDir(), "gone")
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		defer close(done)
		watchWorkspace(ctx, dir, 1, 1, time.Millisecond, func(gitops.WalkStats) {
			t.Error("a missing workspace must not read as a breach")
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
}
