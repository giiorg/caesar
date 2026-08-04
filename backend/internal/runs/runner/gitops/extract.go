package gitops

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// extractIndexName is the throwaway index extraction builds in. It lives in
// the attempt dir, never in either repository, so nothing an agent can reach
// is written to and no later git command inherits it.
const extractIndexName = "extract.index"

// gitlinkMode is the index mode git records for a nested repository. Spec §3
// rejects gitlinks: a commit pointer we cannot resolve is not a patch.
const gitlinkMode = "160000"

// ExtractInput is the extraction contract's input file: the pinned base and
// the caps the traversal and the patch must fit inside.
type ExtractInput struct {
	BaseSHA       string `json:"base_sha"`
	MaxFiles      int64  `json:"max_files"`
	MaxBytes      int64  `json:"max_bytes"`
	MaxPatchBytes int64  `json:"max_patch_bytes"`
}

// ExtractReport is the extraction contract's output file. A typed failure
// sets Outcome; the runner reads this file whatever the exit code was.
type ExtractReport struct {
	Outcome  *runs.Outcome `json:"outcome,omitempty"`
	Diff     string        `json:"diff,omitempty"`
	BaseSHA  string        `json:"base_sha,omitempty"`
	TreeHash string        `json:"tree_hash,omitempty"`
	Changed  bool          `json:"changed"`
}

// Extract computes the canonical patch between the pinned base and whatever
// the agent left in the workspace — without ever executing git against the
// workspace's own .git. Every command runs with GIT_DIR pointed at the
// trusted mirror and the workspace supplied as a plain work tree, so the
// agent's config, hooks and fsmonitor are inert bytes on disk (spec §3
// collection). A directory literally named .git is never added by git, so
// the hostile metadata cannot enter the patch either.
func Extract(ctx context.Context, dir string, in ExtractInput) (ExtractReport, error) {
	ws := filepath.Join(dir, workspaceName)

	// 0. A cap that is not a positive number is a malformed contract, not a
	// licence to run unbounded (BoundedWalk reads a zero cap as no cap).
	if in.MaxFiles <= 0 || in.MaxBytes <= 0 || in.MaxPatchBytes <= 0 {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeExtractionFailed,
			"extraction caps must be positive (files %d, bytes %d, patch %d)", in.MaxFiles, in.MaxBytes, in.MaxPatchBytes)
	}

	// 1. Bound the traversal before git touches anything: an agent that
	// filled the disk must not become an unbounded read.
	stats, err := BoundedWalk(ws, true, in.MaxFiles, in.MaxBytes)
	if err != nil {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeExtractionFailed, "measure workspace: %v", err)
	}
	if stats.Breached {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodePatchTooLarge,
			"workspace exceeds extraction caps (%d files, %d bytes seen; caps %d/%d)",
			stats.Files, stats.Bytes, in.MaxFiles, in.MaxBytes)
	}

	// 2. The trusted mirror is the git dir; the workspace is only a work
	// tree. This is the whole security property of extraction.
	env := append(baseEnv(dir),
		"GIT_DIR="+filepath.Join(dir, baseRepoDir),
		"GIT_WORK_TREE="+ws,
		"GIT_INDEX_FILE="+filepath.Join(dir, extractIndexName),
	)

	// 3. Start the index at the pinned base, then index the workspace as it
	// stands. New blobs land in the trusted mirror, which is deleted with
	// the attempt dir at teardown.
	if _, err := runGit(ctx, dir, env, "read-tree", in.BaseSHA); err != nil {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeExtractionFailed, "read base tree %s: %v", in.BaseSHA, err)
	}
	if _, err := runGit(ctx, dir, env, "add", "-A"); err != nil {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeExtractionFailed, "index workspace: %v", err)
	}
	entries, err := runGit(ctx, dir, env, "ls-files", "-s")
	if err != nil {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeExtractionFailed, "list index: %v", err)
	}
	if path, found := firstGitlink(entries); found {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeEmbeddedRepo,
			"%s is an embedded repository; a gitlink cannot be carried in a patch", path)
	}

	// 4. The tree the workspace amounts to, compared against the base tree:
	// equal means the agent changed nothing that survives to a patch.
	treeOut, err := runGit(ctx, dir, env, "write-tree")
	if err != nil {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeExtractionFailed, "write tree: %v", err)
	}
	tree := strings.TrimSpace(treeOut)
	baseTreeOut, err := runGit(ctx, dir, env, "rev-parse", in.BaseSHA+"^{tree}")
	if err != nil {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeExtractionFailed, "resolve base tree %s: %v", in.BaseSHA, err)
	}
	rep := ExtractReport{BaseSHA: in.BaseSHA, TreeHash: tree, Changed: tree != strings.TrimSpace(baseTreeOut)}
	if !rep.Changed {
		return rep, nil
	}

	// 5. The canonical patch, captured under the cap rather than into
	// memory first: a workspace-sized diff must never be buffered whole.
	diff, truncated, err := gitDiffCapped(ctx, dir, env, in.MaxPatchBytes, in.BaseSHA, tree)
	if err != nil {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodeExtractionFailed, "diff %s..%s: %v", in.BaseSHA, tree, err)
	}
	if truncated {
		return ExtractReport{}, typed(runs.OutcomePermanent, runs.CodePatchTooLarge,
			"patch exceeds the %d byte cap", in.MaxPatchBytes)
	}
	rep.Diff = diff
	return rep, nil
}

// firstGitlink reports the first `git ls-files -s` entry recorded as a nested
// repository. The mode is the first field of every line, so a path with any
// character in it cannot confuse the scan.
func firstGitlink(entries string) (string, bool) {
	for _, line := range strings.Split(entries, "\n") {
		mode, rest, ok := strings.Cut(line, " ")
		if !ok || mode != gitlinkMode {
			continue
		}
		_, path, _ := strings.Cut(rest, "\t")
		return path, true
	}
	return "", false
}

// gitDiffCapped runs diff-tree and keeps at most limit bytes of it, so a
// patch that will be rejected for size is never held in memory in full.
// stdout is captured alone: the patch must be exactly what git wrote, with no
// warning interleaved into it.
func gitDiffCapped(ctx context.Context, dir string, env []string, limit int64, baseSHA, tree string) (string, bool, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "", false, fmt.Errorf("gitops: git binary: %w", err)
	}
	cmd := exec.CommandContext(ctx, gitBin,
		"diff-tree", "-p", "--binary", "--full-index", "-M50", baseSHA, tree)
	cmd.Dir = dir
	cmd.Env = env
	out := &cappedBuffer{limit: limit}
	var errOut strings.Builder
	cmd.Stdout = out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("gitops: git diff-tree: %w: %s", err, errOut.String())
	}
	return out.String(), out.over, nil
}

// cappedBuffer keeps the first limit bytes written to it and remembers that
// more arrived. It keeps draining so the writer never sees a broken pipe —
// the point is bounded memory, not a bounded read.
type cappedBuffer struct {
	limit int64
	buf   []byte
	over  bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - int64(len(b.buf)); room > 0 {
		if int64(len(p)) <= room {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:room]...)
			b.over = true
		}
	} else if len(p) > 0 {
		b.over = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }
