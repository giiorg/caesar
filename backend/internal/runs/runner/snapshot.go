package runner

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
)

const (
	// snapshotTailBytes is how much of the agent's log a failure carries
	// inline: enough for a stack trace or the last exchange, small enough
	// that the snapshot is evidence rather than a second copy of the log.
	snapshotTailBytes = 64 << 10

	// snapshotEntries bounds the workspace listing. The counts beside it
	// measure the whole tree; the listing is the sample an operator reads.
	snapshotEntries = 200

	// snapshotWalkFiles bounds the traversal itself. The workspace being
	// described is often the very thing that breached its quota, so the walk
	// that describes it is capped rather than trusted to end.
	snapshotWalkFiles = 1 << 20

	// redactOverlap is how far behind the tail window the redaction scan
	// starts, so a secret straddling the cut is replaced whole instead of
	// half-shown.
	redactOverlap = 4 << 10
)

// buildFailureSnapshot describes a dead attempt: how its agent container
// ended, the last of what it said, and what it left in the workspace. Every
// string it carries passes through redact first — a snapshot is stored and
// read by humans, so a credential reaching it is a credential leaked.
//
// It is pure and total: a missing log or a vanished workspace yields the
// empty shape, never an error. Something already failed by the time this
// runs, and a snapshot that can fail is a failure that reports nothing.
func buildFailureSnapshot(inspect dockerapi.ContainerInspect, logPath string, tailBytes int64,
	wsDir string, redact []string,
) map[string]any {
	return map[string]any{
		"exit_code":   inspect.State.ExitCode,
		"oom_killed":  inspect.State.OOMKilled,
		"status":      inspect.State.Status,
		"error":       redactAll(inspect.State.Error, redact),
		"started_at":  inspect.State.StartedAt,
		"finished_at": inspect.State.FinishedAt,
		"log_tail":    logTail(logPath, tailBytes, redact),
		"workspace":   workspaceListing(wsDir, redact),
	}
}

// logTail reads the last tailBytes of path, redacted. The read starts a
// little before the window so a secret cut by it is still matched whole; the
// result is then trimmed back to the window.
func logTail(path string, tailBytes int64, redact []string) string {
	if tailBytes <= 0 {
		tailBytes = snapshotTailBytes
	}
	raw := readTail(path, tailBytes+redactOverlap)
	tail := redactAll(string(raw), redact)
	if int64(len(tail)) > tailBytes {
		tail = tail[int64(len(tail))-tailBytes:]
	}
	return tail
}

// readTail returns at most the last n bytes of path, or nothing at all if it
// cannot be read as an ordinary file.
func readTail(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	if off := fi.Size() - n; off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil
		}
	}
	raw, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return nil
	}
	return raw
}

// workspaceListing counts the workspace and names the first entries of it.
// The traversal tolerates every error it meets — an unreadable corner of a
// tree the agent built is a reason to describe the rest, not to give up —
// and it stops at snapshotWalkFiles regardless, because this tree's size is
// the agent's choice.
//
// The top-level .git is skipped, exactly as the quota walk that measured
// this workspace skipped it: the mirror is not the agent's work, and listing
// its objects would spend the whole listing budget on them.
//
// The names are redacted like everything else here: the agent chose them, and
// a file named after its broker token would otherwise smuggle the token into
// an artifact.
func workspaceListing(dir string, redact []string) map[string]any {
	entries := make([]map[string]any, 0, snapshotEntries)
	var files, bytes int64
	scanCapped := false

	topGit := filepath.Join(dir, ".git")
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable corner of the tree is described around, never fatal: see above
		}
		if d.IsDir() {
			if path == topGit {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // a file that vanished mid-walk is one this listing simply does not name
		}
		files++
		bytes += info.Size()
		if len(entries) < snapshotEntries {
			rel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				rel = path
			}
			entries = append(entries, map[string]any{"path": redactAll(rel, redact), "bytes": info.Size()})
		}
		if files >= snapshotWalkFiles {
			scanCapped = true
			return fs.SkipAll
		}
		return nil
	})

	return map[string]any{
		"files":          files,
		"bytes":          bytes,
		"entries":        entries,
		"entries_capped": files > int64(len(entries)),
		"scan_capped":    scanCapped,
	}
}

// redactAll replaces every secret with a marker. The secrets are the
// attempt's own — the broker token and the opened provider credential — and
// they are substrings, not tokens: a credential embedded in a URL or a JSON
// blob must disappear the same way.
func redactAll(s string, redact []string) string {
	for _, secret := range redact {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}
