//go:build integration

package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
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

// The publication work dir is materialized at BOOT, not lazily on the first
// publication. Lazily is the tempting version and the wrong one: it hides a
// misconfiguration until a run has already been approved and pushed, which is
// the worst possible moment to discover it.
//
// A path whose parent is a regular file is a mkdir that cannot succeed, so this
// asserts the boot-time refusal rather than a directory listing — the refusal is
// the observable half, and it can only happen if the directory is made here.
func TestRunServe_RefusesToBootOnAnUnusablePublishWorkDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	cfg.Database.URL = testdb.NewURL(t)
	cfg.Security.MasterKey = "dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg.Forge.PublishWorkDir = filepath.Join(blocker, "publish")

	err := runServe(context.Background(), cfg, discard())
	if err == nil || !strings.Contains(err.Error(), "publish work dir") {
		t.Fatalf("runServe = %v, want a boot refusal naming the publish work dir", err)
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
