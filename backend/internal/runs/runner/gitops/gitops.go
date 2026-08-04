// Package gitops drives the git binary for the trusted containers of the
// runner pipeline. Every command runs with a constructed environment (spec
// §3): git never sees the ambient env, so no user or system config, no
// credential helper and no terminal prompt can reach it.
package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// TypedError carries a typed outcome out of a git operation. The runner
// re-wraps it as runs.OutcomeError; the trusted containers marshal it into
// their report file, which is the only failure signal the runner trusts.
type TypedError struct{ Outcome runs.Outcome }

func (e TypedError) Error() string { return e.Outcome.Code + ": " + e.Outcome.Message }

func typed(class runs.OutcomeClass, code, format string, args ...any) TypedError {
	return TypedError{Outcome: runs.Outcome{Class: class, Code: code, Message: fmt.Sprintf(format, args...)}}
}

// baseEnv is the whole world a git process gets: the git binary's directory,
// a HOME inside the attempt dir (so anything git parks there is caught by the
// negative scan), no global or system config, and no terminal prompt.
func baseEnv(home string) []string {
	gitBin, _ := exec.LookPath("git")
	return []string{
		"PATH=" + filepath.Dir(gitBin),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// runGit runs git with the given constructed env — never the inherited one.
// Errors carry the arguments and git's output, so a caller can classify
// without guessing at exit codes.
func runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("gitops: git binary: %w", err)
	}
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("gitops: git %v: %w: %s", args, err, out)
	}
	return string(out), nil
}

// WalkStats bounds a directory walk: it stops early once either cap is
// exceeded (never an unbounded traversal, spec §3 collection).
type WalkStats struct {
	Files, Bytes int64
	Breached     bool
}

var errWalkBreached = errors.New("walk cap exceeded")

// BoundedWalk counts regular entries under dir, stopping the moment either
// cap is passed. skipTopGit leaves dir's own .git out of the count, which is
// what a workspace quota means: the agent's files, not git's bookkeeping.
func BoundedWalk(dir string, skipTopGit bool, maxFiles, maxBytes int64) (WalkStats, error) {
	var st WalkStats
	topGit := filepath.Join(dir, ".git")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipTopGit && path == topGit {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		st.Files++
		st.Bytes += info.Size()
		if (maxFiles > 0 && st.Files > maxFiles) || (maxBytes > 0 && st.Bytes > maxBytes) {
			st.Breached = true
			return errWalkBreached
		}
		return nil
	})
	if errors.Is(err, errWalkBreached) {
		err = nil
	}
	if err != nil {
		return st, fmt.Errorf("gitops: walk %s: %w", dir, err)
	}
	return st, nil
}

// redact keeps a credential out of any message we build from git's output.
// The scans below exist because a credential must never reach disk; an error
// string is a file the moment it is written to a report.
func redact(msg, credential string) string {
	if credential == "" {
		return msg
	}
	return strings.ReplaceAll(msg, credential, "[redacted]")
}
