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
	CredentialID string `json:"credential_id"`
	Kind         string `json:"credential_kind"`
	Custody      string `json:"custody"`
	// SecretVersion is the version actually delivered. For a leased kind it
	// is the version the acquire transaction granted, and adoption refuses to
	// publish unless the store still holds THIS version: a mid-attempt
	// operator rotation makes the delivered bytes unrecoverable. Kinds that
	// take no lease carry 0 — their bytes come from an open that does not yet
	// report a version, and Plan 07's sync-back work is what gives it one.
	SecretVersion int64  `json:"secret_version"`
	Epoch         int64  `json:"epoch"`                  // leased kinds only; 0 otherwise
	AuthDir       string `json:"auth_dir,omitempty"`     // relPath under DataRoot
	Identity      string `json:"identity,omitempty"`     // pinned account-identity fingerprint (spec §2.5)
	BrokerToken   string `json:"broker_token,omitempty"` // brokered custody only — see writeManifest
}

// Custody is how the attempt's provider credential reaches the harness:
// brokered means only the egress gateway ever holds it, materialized means
// the CLI reads it from a file caesar wrote. The distinction decides who may
// change the credential mid-attempt, so it is recorded rather than re-derived
// at recovery time. Plan 07's driver contract moves the choice onto the
// driver; until then the credential kind is the whole of it.
const (
	custodyBrokered     = "brokered"
	custodyMaterialized = "materialized"
)

// kindAgentAuthFile mirrors the assets credential-kind vocabulary by value:
// the runs context never imports assets, and this is the one kind whose
// bytes a running agent may itself replace.
const kindAgentAuthFile = "agent_auth_file" //nolint:gosec // G101 matches the name: a kind label, not a secret

func custodyFor(kind string) string {
	if kind == kindAgentAuthFile {
		return custodyMaterialized
	}
	return custodyBrokered
}

// manifestFor assembles the manifest from what is already resolved: the
// binding the request pinned, the spec's broker token, and whatever grant the
// lease made (the zero Lease for kinds that take none).
func manifestFor(req runs.ExecutionRequest, spec RunSpec, lease Lease) attemptManifest {
	m := attemptManifest{
		CredentialID:  req.Credential.ID.String(),
		Kind:          req.Credential.Kind,
		Custody:       custodyFor(req.Credential.Kind),
		SecretVersion: lease.SecretVersion,
		Epoch:         lease.Epoch,
	}
	if m.Custody == custodyBrokered {
		m.BrokerToken = spec.BrokerToken
	}
	return m
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
