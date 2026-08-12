package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/giiorg/caesar/backend/internal/realtime"
)

// Every way a stream can end, and what it is worth saying about it. The
// levels are the point: a closed browser tab logged as a fault buries the one
// ending that IS a fault, and a revocation logged as a fault would make an
// ordinary security event look like an outage.
func TestLogStreamEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		// want is the level as slog spells it, or "" for "say nothing at all".
		want string
	}{
		{"a closed tab is the normal end", nil, ""},
		{"a slow consumer reconnects and loses nothing", realtime.CloseSlowConsumer, "INFO"},
		{"the alias is the same reason", realtime.ErrSlowConsumer, "INFO"},
		{"the process going away is routine", realtime.CloseShutdown, "INFO"},
		{"a revocation acted on is routine", realtime.CloseForbidden, "INFO"},
		{"a wrapped reason still matches", fmt.Errorf("serve: %w", realtime.CloseForbidden), "INFO"},
		{"a write failure is a fault", errors.New("write stream frame 3: broken pipe"), "ERROR"},
		{
			// Answered as a 500 as well, but the 500 says nothing and the access
			// log records only a status: this line is where the cause is written
			// down at all.
			"a stream that never opened is a fault",
			fmt.Errorf("%w: realtime: response writer cannot flush: %w", realtime.ErrNotStarted, errors.New("not supported")),
			"ERROR",
		},
		{
			"a re-check out of tolerance is a fault, not a denial",
			fmt.Errorf("stream grant re-check: %w", errors.New("grants store unreachable")),
			"ERROR",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			logStreamEnd(context.Background(), log, tc.err, slog.String("stream", "attention"))

			if tc.want == "" {
				if buf.Len() != 0 {
					t.Fatalf("logged %q, want silence", buf.String())
				}
				return
			}
			var rec struct {
				Level  string `json:"level"`
				Msg    string `json:"msg"`
				Stream string `json:"stream"`
			}
			if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
				t.Fatalf("log line %q is not one JSON record: %v", buf.String(), err)
			}
			if rec.Level != tc.want {
				t.Errorf("level = %q, want %q (line: %s)", rec.Level, tc.want, buf.String())
			}
			if rec.Stream != "attention" {
				t.Errorf("caller attributes were dropped: %s", buf.String())
			}
		})
	}
}
