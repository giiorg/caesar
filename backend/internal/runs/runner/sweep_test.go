package runner

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
)

// sweepEngine answers the two list calls with a fixed inventory and records
// what the sweep asked it to remove. A real daemon stamps Created itself, so
// the age predicate is only readable against a fake.
type sweepEngine struct {
	Engine
	containers []dockerapi.ContainerSummary
	networks   []dockerapi.NetworkSummary

	selectors          []string
	removedC, removedN []string
}

func (f *sweepEngine) ContainerListByLabel(_ context.Context, kv string) ([]dockerapi.ContainerSummary, error) {
	f.selectors = append(f.selectors, kv)
	return f.containers, nil
}

func (f *sweepEngine) NetworkListByLabel(_ context.Context, kv string) ([]dockerapi.NetworkSummary, error) {
	f.selectors = append(f.selectors, kv)
	return f.networks, nil
}

func (f *sweepEngine) ContainerRemove(_ context.Context, id string, _ bool) error {
	f.removedC = append(f.removedC, id)
	return nil
}

func (f *sweepEngine) NetworkRemove(_ context.Context, id string) error {
	f.removedN = append(f.removedN, id)
	return nil
}

func TestSweepOrphans_RemovesOnlyWhatNoLiveRunCanReach(t *testing.T) {
	now := time.Now()
	rfc := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	eng := &sweepEngine{
		containers: []dockerapi.ContainerSummary{
			{ID: "past-the-ceiling", Created: now.Add(-26 * time.Hour).Unix()},
			{ID: "still-reconcilable", Created: now.Add(-time.Hour).Unix()},
		},
		networks: []dockerapi.NetworkSummary{
			{ID: "past-the-ceiling-net", Name: "a-net", Created: rfc(-26 * time.Hour)},
			{ID: "still-reconcilable-net", Name: "b-net", Created: rfc(-time.Minute)},
			// Host-lifetime infrastructure, reused by every attempt: its age
			// says nothing about whether anything still needs it.
			{ID: "shared-egress", Name: defaultEgressNetwork, Created: rfc(-300 * time.Hour)},
			// A network whose age cannot be read is left alone rather than
			// guessed at.
			{ID: "undated", Name: "c-net", Created: "whenever"},
		},
	}
	r, err := New(eng, nil, nil, nil, nil, nil,
		Options{CaesarImage: "caesar:dev", GitImage: "caesar-git:dev", DataRoot: t.TempDir()}, discardLog())
	if err != nil {
		t.Fatal(err)
	}

	if err := r.SweepOrphans(t.Context(), orphanMaxAge); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if diff := cmp.Diff([]string{"past-the-ceiling"}, eng.removedC); diff != "" {
		t.Errorf("removed containers (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"past-the-ceiling-net"}, eng.removedN); diff != "" {
		t.Errorf("removed networks (-want +got):\n%s", diff)
	}
	// Nothing is ever listed unlabeled: another tenant's containers are not
	// this runner's to remove.
	for _, kv := range eng.selectors {
		if kv != managedLabel {
			t.Errorf("sweep listed with selector %q, want %q", kv, managedLabel)
		}
	}
}

func TestRun_TheMaintenanceLoopEndsWithItsContext(t *testing.T) {
	r, err := New(&sweepEngine{}, nil, nil, nil, nil, nil,
		Options{CaesarImage: "caesar:dev", GitImage: "caesar-git:dev", DataRoot: t.TempDir()}, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the maintenance loop outlived its context")
	}
}
