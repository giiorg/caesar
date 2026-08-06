package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// The attempt directory's staging layout: a trusted bare mirror and the
// workspace materialized from it.
const (
	baseRepoDir   = "base.git"
	workspaceName = WorkspaceDir

	// inputPatchFile holds the decoded input patch while git applies it. It
	// lives in the attempt dir, never in the workspace: the agent gets the
	// tree its lineage produced, not the bytes that produced it.
	inputPatchFile = "input.patch"
)

// The harness filesystem contract. caesarDir is workspace-relative and
// reserved: in/ is what the control plane handed this phase, out/ is what the
// harness declares back (see the runner's declared-artifacts reader). It is
// invisible to the extracted patch by two independent mechanisms — the
// exclude line writeCaesarExcludes puts in the trusted mirror, and the
// pathspec Extract passes — either of which suffices alone.
const (
	caesarDir        = ".caesar"
	instructionsFile = "instructions.md"
	inputsFile       = "inputs.json"
)

// The lineage caps, pinned. A breach is a PERMANENT staging_failed: the same
// oversize input would breach the same bound on every retry.
const (
	// maxInputPatchBytes bounds the DECODED patch — the wire form is base64,
	// and the cap belongs to the patch rather than to its encoding. It mirrors
	// the runner's own extraction cap: a patch this side refuses to stage is
	// one the other side would have refused to produce.
	maxInputPatchBytes   = 4 << 20
	maxInstructionsBytes = 256 << 10
	maxContextBytes      = 1 << 20 // per context file
)

// StageContext is one non-patch artifact handed to the phase, materialized as
// a file under .caesar/in. JSON is the artifact payload verbatim.
type StageContext struct {
	Name string          `json:"name"` // "<idx>-<type>.json"
	JSON json.RawMessage `json:"json"`
}

// InputRef is one line of the phase's input manifest: what it was given, and
// — for a context artifact — which file carries it.
type InputRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name,omitempty"` // context filename; empty for applied patches
}

// StageInput is the staging contract's input file. The forge credential is
// deliberately absent: it arrives only through the process environment, so it
// never lands in a file the agent could later read.
type StageInput struct {
	CloneURL   string `json:"clone_url"`
	BaseSHA    string `json:"base_sha"`
	QuotaBytes int64  `json:"quota_bytes"`
	QuotaFiles int64  `json:"quota_files"`

	// The lineage this phase was handed. Patch is SINGULAR by contract: every
	// runner-produced patch diffs from the run's base and therefore subsumes
	// its ancestors, so the newest one alone reproduces the whole linear
	// chain — applying a chain of them would double-apply every ancestor.
	// A node declaring two patch inputs is refused by the runner.
	Instructions string         `json:"instructions,omitempty"`
	TaskInputs   map[string]any `json:"task_inputs,omitempty"`
	Patch        string         `json:"patch,omitempty"` // base64(std) canonical diff
	Contexts     []StageContext `json:"contexts,omitempty"`
	InputRefs    []InputRef     `json:"input_refs,omitempty"`
}

// StageReport is the staging contract's output file. A typed failure sets
// Outcome; the runner reads this file either way, because a container exit
// code alone is never the signal.
type StageReport struct {
	Outcome  *runs.Outcome `json:"outcome,omitempty"`
	HeadSHA  string        `json:"head_sha,omitempty"`
	Files    int64         `json:"files,omitempty"`
	Bytes    int64         `json:"bytes,omitempty"`
	Verified bool          `json:"verified,omitempty"`
}

// Stage clones at a pinned SHA through an in-memory credential path, then
// sanitizes and verifies before any agent can see the result. dir gains
// base.git/ and workspace/ on success; on any verify failure the caller must
// treat the workspace as unusable — a workspace that fails verification is
// never handed to an agent.
func Stage(ctx context.Context, dir string, in StageInput, authEnv []string) (StageReport, error) {
	credential := credentialFrom(authEnv)
	base := filepath.Join(dir, baseRepoDir)
	ws := filepath.Join(dir, workspaceName)
	env := baseEnv(dir)

	// 1. Auth never travels in the URL: a userinfo component would be
	// written into .git/config by clone, so refuse before git runs at all.
	if err := checkCloneURL(in.CloneURL); err != nil {
		return StageReport{}, err
	}

	// 2. Mirror the remote, with the credential supplied as env-only config
	// git does not persist, then prove the pin exists. Only this one command
	// ever sees the credential; every later step runs on the bare env.
	if _, err := runGit(ctx, dir, slices.Concat(env, authEnv), "clone", "-q", "--bare", in.CloneURL, base); err != nil {
		return StageReport{}, typed(runs.OutcomeTransient, runs.CodeStagingFailed, "clone: %s", redact(err.Error(), credential))
	}
	if _, err := runGit(ctx, dir, env, "-C", base, "cat-file", "-e", in.BaseSHA+"^{commit}"); err != nil {
		return StageReport{}, typed(runs.OutcomePermanent, runs.CodeBaseSHAUnavailable,
			"base sha %s is not in the clone of %s", in.BaseSHA, in.CloneURL)
	}

	// 3. Materialize a real working tree with its own .git, so harnesses can
	// run git. It is hostile-by-assumption from here on.
	if _, err := runGit(ctx, dir, env, "clone", "-q", "--no-hardlinks", base, ws); err != nil {
		return StageReport{}, typed(runs.OutcomeTransient, runs.CodeStagingFailed, "materialize workspace: %s", redact(err.Error(), credential))
	}
	if _, err := runGit(ctx, dir, env, "-C", ws, "checkout", "-q", "--detach", in.BaseSHA); err != nil {
		return StageReport{}, typed(runs.OutcomeTransient, runs.CodeStagingFailed, "checkout %s: %s", in.BaseSHA, redact(err.Error(), credential))
	}
	head, err := runGit(ctx, dir, env, "-C", ws, "rev-parse", "HEAD")
	if err != nil {
		return StageReport{}, typed(runs.OutcomeTransient, runs.CodeStagingFailed, "read workspace head: %s", redact(err.Error(), credential))
	}
	if strings.TrimSpace(head) != in.BaseSHA {
		return StageReport{}, typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed,
			"workspace head is %s, want the pinned %s", strings.TrimSpace(head), in.BaseSHA)
	}

	// 4. Sanitize: origin points at the credential-free URL, and neither
	// repository config carries an auth key.
	if _, err := runGit(ctx, dir, env, "-C", ws, "remote", "set-url", "origin", in.CloneURL); err != nil {
		return StageReport{}, typed(runs.OutcomeTransient, runs.CodeStagingFailed, "sanitize origin: %s", redact(err.Error(), credential))
	}
	if err := assertNoCredentialConfig(ctx, dir, base, ws); err != nil {
		return StageReport{}, err
	}

	// 5. Materialize the lineage this phase was handed, and the filesystem
	// contract it reads that lineage through. It happens before the verify so
	// the staged inputs count against the same quota everything else does.
	if err := stageLineage(ctx, dir, ws, env, in); err != nil {
		return StageReport{}, err
	}

	// 6. Verify, then report. The walk is repeated for the report numbers so
	// verifyStaging stays a pure gate with a single job.
	if err := verifyStaging(dir, credential, in.QuotaBytes, in.QuotaFiles); err != nil {
		return StageReport{}, err
	}
	stats, err := BoundedWalk(ws, true, in.QuotaFiles, in.QuotaBytes)
	if err != nil {
		return StageReport{}, typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "measure workspace: %v", err)
	}
	return StageReport{HeadSHA: in.BaseSHA, Files: stats.Files, Bytes: stats.Bytes, Verified: true}, nil
}

// stageLineage materializes what this phase was handed: its predecessor's
// cumulative patch applied to the workspace, then the .caesar contract the
// harness reads its instructions and context artifacts out of.
//
// The order is load-bearing. Every bound is checked before anything is
// written, so a refusal never leaves a half-staged workspace behind; the patch
// lands before .caesar exists, so no patch can reach into the contract
// directory; and the whole thing runs before the report, so every failure
// leaves the attempt with a typed outcome rather than a raw git error.
func stageLineage(ctx context.Context, dir, ws string, env []string, in StageInput) error {
	patch, err := decodeInputPatch(in.Patch)
	if err != nil {
		return err
	}
	if n := len(in.Instructions); n > maxInstructionsBytes {
		return typed(runs.OutcomePermanent, runs.CodeStagingFailed,
			"instructions are %d bytes, past the %d byte cap", n, maxInstructionsBytes)
	}
	for _, c := range in.Contexts {
		if err := checkContextName(c.Name); err != nil {
			return err
		}
		if n := len(c.JSON); n > maxContextBytes {
			return typed(runs.OutcomePermanent, runs.CodeStagingFailed,
				"context %s is %d bytes, past the %d byte cap", c.Name, n, maxContextBytes)
		}
	}

	if len(patch) > 0 {
		// The patch file stays outside the workspace: the agent is handed the
		// tree its lineage produced, never the bytes that produced it.
		path := filepath.Join(dir, inputPatchFile)
		if err := os.WriteFile(path, patch, 0o600); err != nil {
			return typed(runs.OutcomeTransient, runs.CodeStagingFailed, "write input patch: %v", err)
		}
		if _, err := runGit(ctx, dir, env, "-C", ws, "apply", "--whitespace=nowarn", path); err != nil {
			// git's message quotes hunks and paths out of a patch we did not
			// author; the caller gets the fact, not that text.
			return typed(runs.OutcomePermanent, runs.CodeStagingFailed, "input patch does not apply")
		}
	}
	if err := writeCaesarExcludes(dir, ws); err != nil {
		return err
	}
	return writeCaesarInputs(ws, in)
}

// decodeInputPatch turns the base64 wire form into the canonical patch bytes.
// The cap is checked on the DECODED length, which is the patch's real size —
// the encoding is the transport, not the artifact.
func decodeInputPatch(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	if n := base64.StdEncoding.DecodedLen(len(encoded)); n > maxInputPatchBytes+3 {
		// Refuse the obviously oversize without materializing it; the exact
		// check below is what the bound actually is.
		return nil, typed(runs.OutcomePermanent, runs.CodeStagingFailed,
			"input patch is about %d bytes decoded, past the %d byte cap", n, maxInputPatchBytes)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, typed(runs.OutcomePermanent, runs.CodeStagingFailed,
			"input patch is not the base64 wire form the patch artifact carries")
	}
	if len(raw) > maxInputPatchBytes {
		return nil, typed(runs.OutcomePermanent, runs.CodeStagingFailed,
			"input patch is %d bytes decoded, past the %d byte cap", len(raw), maxInputPatchBytes)
	}
	return raw, nil
}

// writeCaesarExcludes hides the contract directory from the patch, twice over.
//
// The mirror's exclude is the authoritative one: extraction indexes the
// workspace with GIT_DIR pointed at base.git, so that file — which lives
// outside the agent's bind mount, and which the agent therefore cannot edit —
// is what governs the diff. The workspace copy is agent-side `git status`
// hygiene and carries no security weight at all: the agent owns that file.
func writeCaesarExcludes(dir, ws string) error {
	for _, gitDir := range []string{filepath.Join(dir, baseRepoDir), filepath.Join(ws, ".git")} {
		info := filepath.Join(gitDir, "info")
		if err := os.MkdirAll(info, 0o755); err != nil {
			return typed(runs.OutcomeTransient, runs.CodeStagingFailed, "create %s: %v", rel(dir, info), err)
		}
		f, err := os.OpenFile(filepath.Join(info, "exclude"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return typed(runs.OutcomeTransient, runs.CodeStagingFailed, "open %s: %v", rel(dir, info)+"/exclude", err)
		}
		// The leading newline starts a line of our own whatever the template
		// left behind; a blank line in an exclude file is ignored.
		_, err = f.WriteString("\n/" + caesarDir + "/\n")
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return typed(runs.OutcomeTransient, runs.CodeStagingFailed, "write %s: %v", rel(dir, info)+"/exclude", err)
		}
	}
	return nil
}

// inputsManifest is .caesar/in/inputs.json: the task's own inputs, and one
// line per artifact this phase was given. Both are always present — a harness
// reads an object and an array whether or not the phase had any lineage.
type inputsManifest struct {
	TaskInputs map[string]any `json:"task_inputs"`
	Inputs     []InputRef     `json:"inputs"`
}

// writeCaesarInputs lays out the contract the harness reads: in/ for what this
// phase was given, out/ empty for what it declares back.
func writeCaesarInputs(ws string, in StageInput) error {
	inDir := filepath.Join(ws, caesarDir, "in")
	outDir := filepath.Join(ws, caesarDir, "out")
	for _, d := range []string{inDir, outDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return typed(runs.OutcomeTransient, runs.CodeStagingFailed, "create %s: %v", filepath.Base(d), err)
		}
	}

	manifest := inputsManifest{TaskInputs: in.TaskInputs, Inputs: in.InputRefs}
	if manifest.TaskInputs == nil {
		manifest.TaskInputs = map[string]any{}
	}
	if manifest.Inputs == nil {
		manifest.Inputs = []InputRef{}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		// TaskInputs came off a stored artifact payload, so a value JSON
		// cannot encode is a malformed input, not a hiccup.
		return typed(runs.OutcomePermanent, runs.CodeStagingFailed, "encode %s: %v", inputsFile, err)
	}

	if err := writeInput(inDir, instructionsFile, []byte(in.Instructions)); err != nil {
		return err
	}
	if err := writeInput(inDir, inputsFile, raw); err != nil {
		return err
	}
	for _, c := range in.Contexts {
		if err := writeInput(inDir, c.Name, c.JSON); err != nil {
			return err
		}
	}
	return nil
}

// writeInput lays down one contract file. 0644 rather than 0600 on purpose:
// these are the phase's inputs, and a harness image is free to run its CLI as
// a different user than the one that staged. They are the control plane's own
// bytes inside a mount only this attempt's agent can see, so nothing here is
// a secret — the credential never reaches the workspace at all.
func writeInput(inDir, name string, data []byte) error {
	if err := os.WriteFile(filepath.Join(inDir, name), data, 0o644); err != nil { //nolint:gosec // G306: world-readable by design, see above
		return typed(runs.OutcomeTransient, runs.CodeStagingFailed, "write %s/in/%s: %v", caesarDir, name, err)
	}
	return nil
}

// checkContextName holds a context artifact to a plain file name. The contract
// file is written by the control plane rather than by an agent, but a name is
// still a name: a path would put an input outside the one directory the phase
// reads its lineage from, and one of the two reserved names would silently
// replace the phase's own instructions.
func checkContextName(name string) error {
	switch {
	case name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`):
		return typed(runs.OutcomePermanent, runs.CodeStagingFailed,
			"context input %q is not a plain file name", name)
	case name == instructionsFile || name == inputsFile:
		return typed(runs.OutcomePermanent, runs.CodeStagingFailed,
			"context input %q is a reserved name in %s/in", name, caesarDir)
	}
	return nil
}

// checkCloneURL refuses anything that could carry a secret into a config
// file: userinfo in the URL, or a URL git would read differently than we do.
func checkCloneURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "clone url is not parseable, so it cannot be proven credential-free")
	}
	if u.User != nil {
		return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "clone url carries userinfo; the credential must travel in the environment only")
	}
	return nil
}

// credentialFrom pulls the header value out of the constructed auth env, so
// the verify scan below searches for the real secret rather than a proxy.
func credentialFrom(authEnv []string) string {
	for _, kv := range authEnv {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_VALUE_0="); ok {
			return v
		}
	}
	return ""
}

// assertNoCredentialConfig proves git persisted no auth into either
// repository. Only the key name reaches the message — never its value.
func assertNoCredentialConfig(ctx context.Context, dir string, repos ...string) error {
	for _, repo := range repos {
		out, err := runGit(ctx, dir, baseEnv(dir), "-C", repo, "config", "--list", "--local")
		if err != nil {
			return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "read %s config: cannot prove it is credential-free", filepath.Base(repo))
		}
		for _, line := range strings.Split(out, "\n") {
			key, _, _ := strings.Cut(line, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			for _, bad := range []string{"extraheader", "askpass", "credential"} {
				if strings.Contains(key, bad) {
					return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed,
						"%s config persisted auth key %s", filepath.Base(repo), key)
				}
			}
		}
	}
	return nil
}

// verifyStaging is the gate: no credential file anywhere under the attempt
// dir, no trace of the credential in either repository's metadata, and a
// workspace that fits its quota. Any failure here is permanent — retrying
// would stage the same thing again.
func verifyStaging(dir, credential string, quotaBytes, quotaFiles int64) error {
	if found, err := findCredentialFile(dir); err != nil {
		return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "scan for credential files: %v", err)
	} else if found != "" {
		return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "credential file present at %s", rel(dir, found))
	}
	if credential != "" {
		for _, root := range []string{filepath.Join(dir, baseRepoDir), filepath.Join(dir, workspaceName, ".git")} {
			found, err := scanForCredential(root, credential)
			if err != nil {
				return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "scan %s: %v", filepath.Base(root), err)
			}
			if found != "" {
				return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "credential persisted at %s", rel(dir, found))
			}
		}
	}
	stats, err := BoundedWalk(filepath.Join(dir, workspaceName), true, quotaFiles, quotaBytes)
	if err != nil {
		return typed(runs.OutcomePermanent, runs.CodeStagingVerificationFailed, "measure workspace: %v", err)
	}
	if stats.Breached {
		return typed(runs.OutcomePermanent, runs.CodeWorkspaceQuotaExceeded,
			"the clone alone exceeds the workspace quota (%d files, %d bytes seen; caps %d/%d)",
			stats.Files, stats.Bytes, quotaFiles, quotaBytes)
	}
	return nil
}

// findCredentialFile looks for a git credential store anywhere under dir —
// including $HOME, which is the attempt dir precisely so this scan sees it.
func findCredentialFile(dir string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == ".git-credentials" {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

// scanForCredential reads every metadata file under root looking for the
// credential. objects/ is excluded: packfiles hold repository content, whose
// bytes are the remote's business and not clone metadata.
func scanForCredential(root, credential string) (string, error) {
	var found string
	needle := []byte(credential)
	objects := filepath.Join(root, "objects")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == objects {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, needle) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if os.IsNotExist(err) {
		return "", nil
	}
	return found, err
}

func rel(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil {
		return r
	}
	return filepath.Base(path)
}
