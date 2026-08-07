package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// maxScanLine bounds the line reassembly buffer. A stream with no newline is
// an agent's choice, not a reason to grow a buffer without limit: once a line
// passes this length it is dropped up to the next newline. Only the gateway's
// own structured lines are scanned, and those are far below it.
const maxScanLine = 64 << 10

// logStreamer copies one container's log stream to one file, under a hard
// byte cap, for as long as the stream lasts. It is started by streamLogs and
// read exactly once through wait.
type logStreamer struct {
	done      chan struct{}
	bytes     int64
	truncated bool
	err       error
}

// wait blocks until the stream ends and reports how many bytes the container
// produced (not how many reached disk), whether the cap truncated the file,
// and any copy error. It is safe to call more than once.
func (s *logStreamer) wait() (int64, bool, error) {
	<-s.done
	return s.bytes, s.truncated, s.err
}

// streamOpts is what one stream needs beyond its container and its file.
// It is a struct because the callbacks are per-role: only the gateway's
// stream is scanned for denials, and only the agent's kills on flood.
type streamOpts struct {
	// stream names the role ("agent"/"gateway") for the truncation event.
	stream string
	// onDenied, when non-nil, is called for every `egress.denied` line the
	// stream carries. It is wired for the gateway alone, so nothing an agent
	// prints can fabricate a denial event.
	onDenied func(host, kind string)
	// onFlood, when non-nil, fires once when the volume this container has
	// produced crosses the flood threshold.
	onFlood func()
}

// streamLogs follows containerID's output into path.
func (r *Runner) streamLogs(ctx context.Context, containerID, path string, ref runs.AttemptRef,
	opts streamOpts,
) *logStreamer {
	s := &logStreamer{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.bytes, s.truncated, s.err = r.copyLogs(ctx, containerID, path, ref, opts)
	}()
	return s
}

func (r *Runner) copyLogs(ctx context.Context, containerID, path string, ref runs.AttemptRef,
	opts streamOpts,
) (int64, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()

	w := &cappedWriter{w: f, limit: r.opts.LogCapBytes}
	if opts.onDenied != nil {
		w.onLine = func(line []byte) {
			if host, kind, ok := parseDenial(line); ok {
				opts.onDenied(host, kind)
			}
		}
	}
	if opts.onFlood != nil {
		w.flood, w.onFlood = r.opts.LogFloodBytes, opts.onFlood
	}

	body, err := r.engine.ContainerLogs(ctx, containerID, true)
	if err != nil {
		return 0, false, err
	}
	defer body.Close()

	// Both halves land in one file: interleaved is how the operator read it
	// on the terminal, and splitting them would lose that ordering.
	copyErr := dockerapi.DemuxLogs(body, w, w)
	w.flush()
	if w.truncated {
		r.emitTruncated(ctx, ref, opts.stream, path, w.total)
	}
	return w.total, w.truncated, copyErr
}

// emitTruncated records the truncation on the run's event stream. A sink
// failure is logged and dropped: the attempt's own result must not hinge on
// bookkeeping about its logs.
//
// The path it names is where the stream is PUBLISHED, not where it is being
// written: an attempt's streams are quarantined while they run, and that
// location is temporary by design. This is the same reference log_ref carries,
// which is the one an operator can follow.
func (r *Runner) emitTruncated(ctx context.Context, ref runs.AttemptRef, stream, path string, produced int64) {
	r.log.Warn("runner.log_truncated", "attempt", ref.String(), "stream", stream,
		"cap_bytes", r.opts.LogCapBytes, "produced_bytes", produced)
	if r.events == nil {
		return
	}
	err := r.events.AppendRuntimeEvent(context.WithoutCancel(ctx), ref, "phase.log_truncated", map[string]any{
		"stream":         stream,
		"path":           r.relPath(filepath.Join(r.logsDir(ref), filepath.Base(path))),
		"cap_bytes":      r.opts.LogCapBytes,
		"produced_bytes": produced,
	})
	if err != nil {
		r.log.Warn("runner.log_truncated.event", "attempt", ref.String(), "error", err)
	}
}

// cappedWriter writes at most limit bytes through to w while counting every
// byte offered, so a truncated log still reports the true volume. Its writes
// come from a single goroutine (the demuxer), so it needs no locking.
type cappedWriter struct {
	w     io.Writer
	limit int64

	written   int64
	total     int64
	truncated bool

	// flood is the volume at which onFlood fires, once. It is measured
	// against total rather than written: past the cap the bytes are dropped,
	// and an agent that could outrun the flood kill by first exceeding the
	// truncation cap would have the enforcement exactly backwards.
	flood   int64
	onFlood func()
	flooded bool

	onLine   func([]byte)
	line     []byte
	dropping bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if c.onFlood != nil && !c.flooded && c.flood > 0 && c.total >= c.flood {
		c.flooded = true
		c.onFlood()
	}
	if c.onLine != nil {
		c.scan(p)
	}
	room := c.limit - c.written
	if room <= 0 {
		c.truncated = len(p) > 0 || c.truncated
		return len(p), nil
	}
	chunk := p
	if int64(len(chunk)) > room {
		chunk, c.truncated = chunk[:room], true
	}
	n, err := c.w.Write(chunk)
	c.written += int64(n)
	if err != nil {
		return n, err
	}
	// The caller is io.CopyBuffer: a short count is ErrShortWrite, and the
	// bytes past the cap were dropped on purpose, not lost.
	return len(p), nil
}

// scan splits the stream into lines for onLine, tolerating a line torn
// across frames.
func (c *cappedWriter) scan(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			c.appendLine(p)
			return
		}
		c.appendLine(p[:i])
		c.emitLine()
		p = p[i+1:]
	}
}

func (c *cappedWriter) appendLine(b []byte) {
	if c.dropping {
		return
	}
	if len(c.line)+len(b) > maxScanLine {
		c.dropping, c.line = true, c.line[:0]
		return
	}
	c.line = append(c.line, b...)
}

func (c *cappedWriter) emitLine() {
	if !c.dropping && len(c.line) > 0 {
		c.onLine(c.line)
	}
	c.line, c.dropping = c.line[:0], false
}

// flush surfaces a final line the stream ended without a newline on.
func (c *cappedWriter) flush() {
	if c.onLine != nil {
		c.emitLine()
	}
}

// denialLine is the subset of the gateway's `egress.denied` record the runner
// turns into a phase event. The gateway logs slog JSON, one object per line.
type denialLine struct {
	Msg  string `json:"msg"`
	Kind string `json:"kind"`
	Host string `json:"host"`
}

func parseDenial(line []byte) (host, kind string, ok bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return "", "", false
	}
	var d denialLine
	if err := json.Unmarshal(line, &d); err != nil || d.Msg != "egress.denied" {
		return "", "", false
	}
	return d.Host, d.Kind, true
}
