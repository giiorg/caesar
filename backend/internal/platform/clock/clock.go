// Package clock is the injected time seam (decisions §9): production code
// takes a Clock, tests take a Fake. First consumers: session expiry and
// login rate limiting (identity).
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

// System is the production clock.
type System struct{}

func (System) Now() time.Time { return time.Now() }

// Fake is a manually-advanced clock for tests. Safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

func NewFake(t0 time.Time) *Fake { return &Fake{now: t0} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}
