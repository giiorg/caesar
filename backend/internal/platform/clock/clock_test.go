package clock_test

import (
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/clock"
)

func TestSystem_TracksRealTime(t *testing.T) {
	before := time.Now()
	got := clock.System{}.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("System.Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestFake_AdvanceAndSet(t *testing.T) {
	t0 := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(t0)
	if got := fake.Now(); !got.Equal(t0) {
		t.Fatalf("Now() = %v, want %v", got, t0)
	}
	fake.Advance(90 * time.Minute)
	if got, want := fake.Now(), t0.Add(90*time.Minute); !got.Equal(want) {
		t.Fatalf("after Advance: Now() = %v, want %v", got, want)
	}
	t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fake.Set(t1)
	if got := fake.Now(); !got.Equal(t1) {
		t.Fatalf("after Set: Now() = %v, want %v", got, t1)
	}
}
