package gitops

import (
	"bytes"
	"context"
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
	workspaceName = "workspace"
)

// StageInput is the staging contract's input file. The forge credential is
// deliberately absent: it arrives only through the process environment, so it
// never lands in a file the agent could later read.
type StageInput struct {
	CloneURL   string `json:"clone_url"`
	BaseSHA    string `json:"base_sha"`
	QuotaBytes int64  `json:"quota_bytes"`
	QuotaFiles int64  `json:"quota_files"`
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

	// 5. Verify, then report. The walk is repeated for the report numbers so
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
