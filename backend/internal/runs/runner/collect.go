package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

const (
	// maxPatchBytes is the ceiling on one attempt's patch (spec §3): a
	// change larger than this is not a review unit.
	maxPatchBytes = 4 << 20

	// stageReportLimit bounds a handful of fields. extractReportLimit must
	// clear the patch cap inflated by base64 (~4/3) plus the report's other
	// fields — the patch is the one contract field that is not small.
	stageReportLimit   = 1 << 20
	extractReportLimit = 8 << 20

	// declaredArtifactsLimit bounds what a harness may hand back through the
	// workspace. Artifacts are documents, not payload channels.
	declaredArtifactsLimit = 1 << 20
	declaredArtifactsPath  = ".caesar/out/artifacts.json"
)

// collectResult turns a finished agent container into the attempt's result:
// the canonical patch from a trusted extraction container, whatever the
// harness declared, and the log reference. It is shared with reconciliation's
// adoption path, which arrives here with an exit code it read off a container
// this process never started.
func collectResult(ctx context.Context, r *Runner, req runs.ExecutionRequest, spec RunSpec,
	exitCode int64, oom bool,
) (runs.ExecutionResult, error) {
	ref := req.Ref
	// A nonzero exit is transient here on purpose: this layer knows only
	// that the process failed. Reading meaning into a harness's exit codes
	// is the harness adapter's job (Plan 07).
	if exitCode != 0 {
		return runs.ExecutionResult{}, transientf(runs.CodeHarnessError,
			"harness exited %d (oom_killed=%v)", exitCode, oom)
	}

	rep, err := r.extractPatch(ctx, ref, spec)
	if err != nil {
		return runs.ExecutionResult{}, err
	}
	declared, err := r.declaredArtifacts(ref)
	if err != nil {
		return runs.ExecutionResult{}, err
	}

	artifacts := make([]runs.NewArtifact, 0, len(declared)+2)
	if rep.Changed {
		artifacts = append(artifacts, runs.NewArtifact{
			Type: runs.ArtifactKindPatch,
			Payload: map[string]any{
				// diff is base64 by contract. A patch is arbitrary bytes — a
				// file in a legacy encoding is diffed as text, so its raw
				// non-UTF-8 bytes land in the patch — and this payload is
				// stored as JSON, which replaces invalid UTF-8 in a string
				// with U+FFFD. That corruption is silent: the mangled patch
				// still applies. Consumers (Plan 08's publish path) decode
				// once; nothing round-trips it through a Go string.
				"diff":      base64.StdEncoding.EncodeToString(rep.Diff),
				"base_sha":  req.BaseSHA,
				"tree_hash": rep.TreeHash,
			},
		})
	}
	artifacts = append(artifacts, declared...)
	artifacts = append(artifacts, r.logRefArtifact(ref))
	return runs.ExecutionResult{Artifacts: artifacts}, nil
}

// extractPatch runs the trusted extraction container: no network, the whole
// attempt directory bound, and git pointed at the mirror the agent never
// touched. Its report is the only thing believed about the workspace.
func (r *Runner) extractPatch(ctx context.Context, ref runs.AttemptRef, spec RunSpec) (gitops.ExtractReport, error) {
	dir := r.attemptDir(ref)
	in := gitops.ExtractInput{
		BaseSHA:       spec.BaseSHA,
		MaxFiles:      r.opts.WorkspaceQuotaFiles,
		MaxBytes:      r.opts.WorkspaceQuotaBytes,
		MaxPatchBytes: maxPatchBytes,
	}
	if err := writeContract(filepath.Join(dir, gitops.ExtractInputFile), in); err != nil {
		return gitops.ExtractReport{}, transientf(runs.CodeExtractionFailed, "write extract contract: %v", err)
	}

	code, runErr := r.runToCompletion(ctx, extractName(ref), dockerapi.ContainerConfig{
		Image:  r.opts.GitImage,
		Cmd:    []string{"extract"},
		User:   spec.User,
		Labels: labels(ref),
	}, r.hardenedHost([]string{r.hostPath(dir) + ":" + gitops.AttemptMount},
		noNetwork, gitMemoryBytes, gitNanoCPUs, gitPidsLimit), nil, extractTimeout)

	rep, readErr := readContract[gitops.ExtractReport](filepath.Join(dir, gitops.ExtractReportFile), extractReportLimit)
	if readErr != nil {
		if runErr != nil {
			return gitops.ExtractReport{}, transientf(runs.CodeExtractionFailed, "extract container: %v", runErr)
		}
		return gitops.ExtractReport{}, transientf(runs.CodeExtractionFailed,
			"extract container exited %d without a readable report: %v", code, readErr)
	}
	if rep.Outcome != nil {
		return gitops.ExtractReport{}, runs.OutcomeError{Outcome: *rep.Outcome}
	}
	return rep, nil
}

// declaredArtifact is what a harness may hand back through the workspace: a
// kind and an opaque payload, nothing about identity or lineage.
type declaredArtifact struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// declaredArtifacts reads the harness's own outputs out of the workspace.
// The read is rooted at the workspace, so a .caesar path that turns out to be
// a symlink onto the host reads nothing: the agent controls every byte of
// that directory, including its shape.
func (r *Runner) declaredArtifacts(ref runs.AttemptRef) ([]runs.NewArtifact, error) {
	root, err := os.OpenRoot(filepath.Join(r.attemptDir(ref), gitops.WorkspaceDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, transientf(runs.CodeInternalError, "open workspace: %v", err)
	}
	defer root.Close()

	f, err := root.Open(declaredArtifactsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // declaring artifacts is optional
		}
		return nil, permanentf(runs.CodeInvalidOutput, "open %s: %v", declaredArtifactsPath, err)
	}
	defer f.Close()

	// A pipe or a device here would turn a read into a wait; only a real
	// file is an artifact declaration.
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, permanentf(runs.CodeInvalidOutput, "%s is not a regular file", declaredArtifactsPath)
	}

	var declared []declaredArtifact
	dec := json.NewDecoder(io.LimitReader(f, declaredArtifactsLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&declared); err != nil {
		return nil, permanentf(runs.CodeInvalidOutput, "parse %s: %v", declaredArtifactsPath, err)
	}

	out := make([]runs.NewArtifact, 0, len(declared))
	for i, d := range declared {
		if d.Type == runs.ArtifactKindPatch {
			return nil, permanentf(runs.CodeInvalidOutput,
				"%s[%d]: a harness may not declare a patch — the patch is extracted from the workspace", declaredArtifactsPath, i)
		}
		if !declarable(d.Type) {
			return nil, permanentf(runs.CodeInvalidOutput,
				"%s[%d]: unknown artifact type %q", declaredArtifactsPath, i, d.Type)
		}
		out = append(out, runs.NewArtifact{Type: d.Type, Payload: d.Payload})
	}
	return out, nil
}

// declarable is the allowlist of kinds a harness may produce. log_ref and
// failure_snapshot are the runtime's own observations, and the patch is
// extracted, so neither can be asserted from inside the sandbox.
func declarable(kind string) bool {
	return kind == runs.ArtifactKindPlan || kind == runs.ArtifactKindReviewVerdict
}

// logRefArtifact points at the attempt's captured streams. The sizes are read
// off disk rather than carried out of the streamers, so reconciliation's
// adoption path — which never ran a streamer — produces the same artifact.
// A file that landed exactly on the cap therefore reads as truncated: the
// conservative direction, since the alternative claims completeness the
// bytes on disk cannot support.
func (r *Runner) logRefArtifact(ref runs.AttemptRef) runs.NewArtifact {
	streams := map[string]any{}
	for name, file := range map[string]string{"agent": agentLogFile, "gateway": gatewayLogFile} {
		path := filepath.Join(r.logsDir(ref), file)
		entry := map[string]any{"path": r.relPath(path), "bytes": int64(0), "truncated": false}
		if fi, err := os.Stat(path); err == nil {
			entry["bytes"] = fi.Size()
			entry["truncated"] = fi.Size() >= r.opts.LogCapBytes
		}
		streams[name] = entry
	}
	return runs.NewArtifact{Type: runs.ArtifactKindLogRef, Payload: map[string]any{
		"streams": streams,
		// The gateway's metered usage is harvested into this map by the
		// enforcement task; the key exists from the start so no consumer has
		// to special-case its absence.
		"egress": map[string]any{},
	}}
}

// writeContract hands one input file to a trusted container.
func writeContract(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readContract reads one report file back: strictly (an unknown field means
// the two halves disagree about the contract) and under a cap.
func readContract[T any](path string, limit int64) (T, error) {
	var v T
	f, err := os.Open(path)
	if err != nil {
		return v, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}
