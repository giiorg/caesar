package runner

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// manifestLimit bounds a strict read of a file this process wrote: the
// manifest holds a handful of short fields, and the one variable-length
// value (the broker token) is a fixed-width random string.
const manifestLimit = 128 << 10

// attemptManifest records what reconciliation and adoption need to know
// about the attempt's credential material without resolving anything through
// agent-writable state (spec §2.3). Convenience, not authority — the lease
// row is the recovery authority; this file may be missing after a crash.
// Written for EVERY attempt with a credential binding (all kinds, both
// custodies), not only leased ones: crash adoption needs the credential id,
// kind, and custody to rebuild the redaction set — without a manifest it must
// withhold the attempt's logs and artifacts.
type attemptManifest struct {
	CredentialID string  `json:"credential_id"`
	Kind         string  `json:"credential_kind"`
	Custody      custody `json:"custody"`
	// HarnessKind is adoption's route back to the driver: without it a
	// crash-adopted nonzero exit has no marker table to read, and a cap hit
	// would be charged to the node as a generic harness failure.
	HarnessKind string `json:"harness_kind"`
	// SecretVersion is the version actually delivered — the acquire
	// transaction's grant for a leased kind, the versioned open's answer for
	// every other. Adoption refuses to publish unless the store still holds
	// THIS version: a mid-attempt operator rotation makes the delivered bytes
	// unrecoverable.
	SecretVersion int64  `json:"secret_version"`
	Epoch         int64  `json:"epoch"`                  // leased kinds only; 0 otherwise
	AuthDir       string `json:"auth_dir,omitempty"`     // relPath under DataRoot
	Identity      string `json:"identity,omitempty"`     // pinned account-identity fingerprint (spec §2.5)
	BrokerToken   string `json:"broker_token,omitempty"` // brokered custody only — see writeManifest

	// secretBytes is the credential as materialized, held in memory for the
	// length of the attempt and never serialized (unexported): sync-back
	// compares the read-back file against it to tell a CLI refresh from a file
	// nothing touched. The provider credential deliberately does not go into
	// the file — a recovery path that lacks these bytes skips the comparison
	// and settles the question through the CAS instead.
	secretBytes []byte
}

// grant is the attempt's provider credential as actually resolved: the bytes
// the harness will be handed, the store version they belong to, and the lease
// epoch guarding them (0 for kinds that take no lease). The three travel
// together because every recovery path needs all three or none.
type grant struct {
	secret  []byte
	version int64
	epoch   int64
}

// kindAgentAuthFile mirrors the assets credential-kind vocabulary by value:
// the runs context never imports assets, and this is the one kind whose
// bytes a running agent may itself replace.
const kindAgentAuthFile = "agent_auth_file" //nolint:gosec // G101 matches the name: a kind label, not a secret

// manifestFor assembles the manifest from what is already resolved: the
// binding the request pinned, the custody and broker token the spec already
// carries (the driver derived it from the harness/credential pair — recovery
// reads it here rather than re-deriving it), and the credential as delivered.
//
// The auth directory and the account identity are the materialized half, and
// they are gated on CUSTODY rather than on the harness: the same Codex driver
// holding a model key brokers it, and a raw API key is not an auth.json —
// feeding one to the identity extractor would be a bug, not a fallback.
//
// Extracting the identity here, before anything runs, is deliberate: it is the
// value read-back compares against (spec §2.5), so a credential caesar cannot
// pin an identity to could never have a refresh committed. Discovering that at
// the end of an attempt would cost the whole attempt.
func (r *Runner) manifestFor(req runs.ExecutionRequest, spec RunSpec, d driver, g grant) (attemptManifest, error) {
	m := attemptManifest{
		CredentialID:  req.Credential.ID.String(),
		Kind:          req.Credential.Kind,
		Custody:       spec.Custody,
		HarnessKind:   spec.Harness.Kind,
		SecretVersion: g.version,
		Epoch:         g.epoch,
		secretBytes:   g.secret,
	}
	if m.Custody == custodyBrokered {
		m.BrokerToken = spec.BrokerToken
		return m, nil
	}
	if d.authMount == nil { // env-delivered: nothing on disk, nothing to read back
		return m, nil
	}
	if d.identity != nil {
		identity, err := d.identity(g.secret)
		if err != nil {
			return attemptManifest{}, permanentf(runs.CodeAuthExpired,
				"credential %s cannot be identity-pinned: %v", req.Credential.ID, err)
		}
		m.Identity = identity
	}
	m.AuthDir = r.relPath(r.authDir(req.Ref))
	return m, nil
}

// writeManifest publishes one attempt's manifest at 0600.
//
// The broker token is the one secret the manifest carries, and only for
// brokered attempts: the per-attempt gateway token is generated in-process
// and exists nowhere else, so a crashed worker's replacement could never
// redact it from quarantined logs otherwise (it rides the agent's env, so it
// can appear there). Persisting it is acceptable where persisting provider
// credentials is not: the file is 0600, host-only, outside every bind (the
// agent cannot reach it), and the token is dead the moment the attempt's
// gateway is gone — it is redaction material, not a live credential. Provider
// credentials are deliberately NOT serialized; adoption re-opens them.
//
// The write is atomic (temp file, then rename): recovery is written for a
// manifest that is present or absent, and a half-written one would fail the
// strict read and look like corruption rather than the documented gap.
func (r *Runner) writeManifest(ref runs.AttemptRef, m attemptManifest) error {
	dir := r.manifestsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create manifests dir: %w", err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ref.String()+".*.tmp")
	if err != nil {
		return fmt.Errorf("create manifest temp: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod manifest temp: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close manifest: %w", err)
	}
	if err := os.Rename(tmp.Name(), r.manifestPath(ref)); err != nil {
		return fmt.Errorf("publish manifest: %w", err)
	}
	return nil
}

// readManifest is the recovery-side read: strict, so a manifest this build
// does not understand is refused rather than half-believed.
func (r *Runner) readManifest(ref runs.AttemptRef) (attemptManifest, error) {
	return readContract[attemptManifest](r.manifestPath(ref), manifestLimit)
}
