package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"

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

// streamLogs follows containerID's output into path. stream names the role
// ("agent"/"gateway") for the truncation event. onDenied, when non-nil, is
// called for every `egress.denied` line the stream carries — it is wired for
// the gateway alone, so nothing an agent prints can fabricate a denial event.
func (r *Runner) streamLogs(ctx context.Context, containerID, path string, ref runs.AttemptRef,
	stream string, onDenied func(host, kind string),
) *logStreamer {
	s := &logStreamer{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.bytes, s.truncated, s.err = r.copyLogs(ctx, containerID, path, ref, stream, onDenied)
	}()
	return s
}

func (r *Runner) copyLogs(ctx context.Context, containerID, path string, ref runs.AttemptRef,
	stream string, onDenied func(host, kind string),
) (int64, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()

	w := &cappedWriter{w: f, limit: r.opts.LogCapBytes}
	if onDenied != nil {
		w.onLine = func(line []byte) {
			if host, kind, ok := parseDenial(line); ok {
				onDenied(host, kind)
			}
		}
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
		r.emitTruncated(ctx, ref, stream, path, w.total)
	}
	return w.total, w.truncated, copyErr
}

// emitTruncated records the truncation on the run's event stream. A sink
// failure is logged and dropped: the attempt's own result must not hinge on
// bookkeeping about its logs.
func (r *Runner) emitTruncated(ctx context.Context, ref runs.AttemptRef, stream, path string, produced int64) {
	r.log.Warn("runner.log_truncated", "attempt", ref.String(), "stream", stream,
		"cap_bytes", r.opts.LogCapBytes, "produced_bytes", produced)
	if r.events == nil {
		return
	}
	err := r.events.AppendRuntimeEvent(context.WithoutCancel(ctx), ref, "phase.log_truncated", map[string]any{
		"stream":         stream,
		"path":           r.relPath(path),
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

	onLine   func([]byte)
	line     []byte
	dropping bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	c.total += int64(len(p))
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
