package realtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/config"
)

// Three files in this package each repeat a config.Realtime default as their
// own literal — hub.go, tailer.go and stream.go — so that a zero HubConfig,
// TailerConfig or StreamerConfig is usable rather than broken. That is two
// definitions of one number, deliberately, and THIS is the tie-back: it fails
// the moment either end moves alone.
//
// It is a test rather than a constant reference because the dependency cannot
// go either way in production. config is platform and must not import a
// bounded context; and this package must not read config to decide what its
// own zero value means, or the zero would stop being usable without one. A
// test file is the only place both names are legal, so a failing test is the
// strongest tie available here — say so rather than call it compile-time.
//
// defaultBatch is absent on purpose: TailerConfig.Batch is how many rows one
// read of the table takes and has no config.Realtime knob at all. See
// internal/app.startRealtime for why it is not ReplayBatch.
func TestDefaults_MatchConfigRealtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caesar.json")
	if err := os.WriteFile(path, []byte(`{"database": {"url": "postgres://x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		knob      string
		here, cfg any
	}{
		{"realtime.heartbeat_interval", defaultHeartbeatInterval, cfg.Realtime.HeartbeatInterval.Std()},
		{"realtime.poll_fallback", defaultPollFallback, cfg.Realtime.PollFallback.Std()},
		{"realtime.grant_recheck", defaultGrantRecheck, cfg.Realtime.GrantRecheck.Std()},
		{"realtime.subscriber_queue", defaultSubscriberQueue, cfg.Realtime.SubscriberQueue},
		{"realtime.replay_batch", defaultReplayBatch, cfg.Realtime.ReplayBatch},
	}
	for _, tt := range tests {
		if tt.here != tt.cfg {
			t.Errorf("%s: this package defaults to %v, config defaults to %v — one end moved "+
				"without the other, and a zero config no longer means what the config file says",
				tt.knob, tt.here, tt.cfg)
		}
	}
}
