// Package gitops drives the git binary for the trusted parts of the run
// pipeline. Every command runs with a constructed environment (spec §3): git
// never sees the ambient env, so no user or system config, no credential
// helper and no terminal prompt can reach it.
//
// Two callers live here for one reason. Stage and Extract run INSIDE the
// caesar-git container because they touch a workspace and a .git the agent
// controlled. Publish does not — it reads the remote at a pinned commit and
// applies a canonical patch — and runs in the control-plane process. It lives
// in this package because this is where the git-driving primitives are, not
// because it is a runner concern; the runner/ path is an accident of birth.
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

// The attempt directory as the trusted containers see it, and the names of
// the contract files inside it. They live here because both halves of every
// contract need the same spelling: the container-side command writes the
// report, the runner writes the input and reads the report back, and the
// runner may not import the composition root that hosts the commands.
const (
	AttemptMount      = "/attempt"
	StageInputFile    = "stage.json"
	StageReportFile   = "stage.report.json"
	ExtractInputFile  = "extract.json"
	ExtractReportFile = "extract.report.json"

	// WorkspaceDir is the staged workspace inside the attempt directory: the
	// one path the agent container shares with the control plane.
	WorkspaceDir = "workspace"
)

// TypedError carries a typed outcome out of a git operation. The runner
// re-wraps it as runs.OutcomeError; the trusted containers marshal it into
// their report file, which is the only failure signal the runner trusts.
type TypedError struct{ Outcome runs.Outcome }

func (e TypedError) Error() string { return e.Outcome.Code + ": " + e.Outcome.Message }

// AsOutcomeError rewraps a TypedError as the runs.OutcomeError the spine
// classifies, and returns anything else untouched.
//
// It exists because runs.ClassifyOutcome matches runs.OutcomeError and nothing
// else, and is default-deny: a TypedError handed to it straight becomes a
// PERMANENT internal_error. For the publish path that is not cosmetic — it is
// the difference between halting a conflicted marker for a human and killing
// the run, and between retrying a rejected push and terminalizing a run whose
// branch may already be on the forge. Every adapter that returns a gitops
// error across the runs.GitPublisher port must call this; the runner's
// container entrypoints do the equivalent through their report files.
func AsOutcomeError(err error) error {
	var te TypedError
	if !errors.As(err, &te) {
		return err
	}
	return runs.OutcomeError{Outcome: te.Outcome}
}

func typed(class runs.OutcomeClass, code, format string, args ...any) TypedError {
	return TypedError{Outcome: runs.Outcome{Class: class, Code: code, Message: fmt.Sprintf(format, args...)}}
}

// baseEnv is the whole world a git process gets: the git binary's directory,
// a HOME inside the attempt dir (so anything git parks there is caught by the
// negative scan), no global or system config, and no terminal prompt.
//
// The identity is pinned rather than left to git. Left to itself, git builds
// a default committer from the host — and canonicalizing that hostname is a
// DNS lookup, which in a container with no route to its nameserver stalls
// every reflog-writing command for the resolver's full timeout.
func baseEnv(home string) []string {
	gitBin, _ := exec.LookPath("git")
	return []string{
		"PATH=" + filepath.Dir(gitBin),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=caesar", "GIT_AUTHOR_EMAIL=caesar@invalid",
		"GIT_COMMITTER_NAME=caesar", "GIT_COMMITTER_EMAIL=caesar@invalid",
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
