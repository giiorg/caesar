//go:build integration

package app

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

// syncBuffer captures runServe's log while its goroutines are still writing.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunServe_BootsMigratesAndShutsDownOnCancel(t *testing.T) {
	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	cfg.Database.URL = testdb.NewURL(t)
	cfg.Security.MasterKey = "dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg.Orchestrator = config.Orchestrator{
		Workers:           2,
		PollInterval:      config.Duration(50 * time.Millisecond),
		LeaseTTL:          config.Duration(30 * time.Second),
		HeartbeatInterval: config.Duration(10 * time.Second),
		OutboxRetention:   config.Duration(168 * time.Hour),
	}

	logBuf := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, log) }()

	// Cancel only after boot has finished: the listening line lands after
	// migrations apply, where a fixed sleep raced a slow migrate on CI.
	bootDeadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(logBuf.String(), "http server listening") {
		select {
		case err := <-done:
			t.Fatalf("runServe returned before listening: %v\n%s", err, logBuf.String())
		default:
		}
		if time.Now().After(bootDeadline) {
			t.Fatalf("server never reported listening:\n%s", logBuf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe = %v, want clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not return after cancel")
	}
}

func TestRunServe_RefusesToBootWithoutMasterKey(t *testing.T) {
	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	cfg.Database.URL = testdb.NewURL(t)

	err := runServe(context.Background(), cfg, discard())
	if err == nil || !strings.Contains(err.Error(), "security.master_key") {
		t.Fatalf("runServe = %v, want error mentioning security.master_key", err)
	}
}

func TestRoutes_HealthzAgainstRealDatabase(t *testing.T) {
	pool := testdb.New(t)
	resp, body := getHealthzWithPinger(t, pool)
	if resp.StatusCode != 200 || body["db"] != "ok" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
}
