package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/giiorg/caesar/backend/internal/runs"
)

const (
	// minSecretLen is the exact-match floor (spec §3): an auth.json also holds
	// benign short values — `auth_mode: "chatgpt"`, timestamps — and matching
	// those exactly would blank or fail legitimate patches. Nothing shorter
	// than this is treated as a secret; the per-kind sensitive-field list is
	// what makes the floor safe to have.
	minSecretLen = 16

	// redactedMarker replaces every matched value. It names caesar so an
	// operator reading a published log knows the gap is deliberate.
	redactedMarker = "[caesar:redacted]"
)

// sensitiveSet is the attempt's exact-bytes redaction list: the broker token,
// the provider credential, and the driver's sensitive fields — initial AND
// refreshed, because a token the CLI rotates mid-run is unknown to caesar
// until the read-back reports it (spec §3). It is built once per attempt and
// completed at the conclusion; nothing scans or publishes before then.
type sensitiveSet struct{ values [][]byte }

// newSensitiveSet builds the set from the values already in hand. Anything
// below the floor is dropped, so a caller may hand over whatever it has
// without first deciding what counts as a secret.
func newSensitiveSet(values ...[]byte) *sensitiveSet {
	s := &sensitiveSet{}
	s.add(values...)
	return s
}

// add extends the set. Duplicates are dropped: the same credential arrives
// from several directions (the raw bytes, the driver's field list, the
// read-back), and scanning for it twice only costs time.
func (s *sensitiveSet) add(values ...[]byte) {
	for _, v := range values {
		if len(v) < minSecretLen {
			continue
		}
		if slices.ContainsFunc(s.values, func(have []byte) bool { return bytes.Equal(have, v) }) {
			continue
		}
		s.values = append(s.values, bytes.Clone(v))
	}
}

// scan reports whether any value in the set appears in b. The bytes handed
// here must be the CANONICAL ones — the decoded patch, not its base64 payload
// (spec §3) — or the match can never happen.
func (s *sensitiveSet) scan(b []byte) bool {
	for _, v := range s.values {
		if bytes.Contains(b, v) {
			return true
		}
	}
	return false
}

// redact replaces every occurrence with the marker. The values are substrings,
// not tokens: a credential embedded in a URL or a JSON blob disappears the
// same way.
func (s *sensitiveSet) redact(b []byte) []byte {
	for _, v := range s.values {
		b = bytes.ReplaceAll(b, v, []byte(redactedMarker))
	}
	return b
}

// strings renders the set for the string-level redaction the failure snapshot
// does (snapshot.go's redactAll). The two redactors share one source of truth:
// a value the scan knows and the snapshot does not would leak through the very
// artifact an operator reads.
func (s *sensitiveSet) strings() []string {
	out := make([]string, 0, len(s.values))
	for _, v := range s.values {
		out = append(out, string(v))
	}
	return out
}

// publishLogs redacts the quarantined streams and publishes them into the logs
// directory the log_ref artifact points at. It is the ONLY thing that puts an
// attempt's log bytes there, which is what makes the guarantee mechanical:
// every published byte passed a complete set, because a caller with an
// incomplete one withholds instead of calling this (spec §3).
//
// Each stream is written to a temp file and renamed, so a reader following the
// published path sees either nothing or the whole redacted stream. The
// quarantine is removed last: it is the recovery copy until publication is
// complete.
//
// Idempotent — a missing quarantine publishes nothing — so the retry of a
// worker that died mid-publish is a no-op over what already landed.
func (r *Runner) publishLogs(ref runs.AttemptRef, set *sensitiveSet) error {
	if set == nil {
		// Unreachable by construction; fail closed rather than publish bytes
		// nothing scanned.
		return fmt.Errorf("runner: publish logs for %s without a redaction set", ref)
	}
	quarantine := r.quarantineDir(ref)
	entries, err := os.ReadDir(quarantine)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read quarantine %s: %w", quarantine, err)
	}
	logs := r.logsDir(ref)
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}
	for _, e := range entries {
		// Only the ordinary files this runner streamed are publishable; the
		// quarantine is ours alone, and anything else in it is not a log.
		if !e.Type().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(quarantine, e.Name()))
		if err != nil {
			return fmt.Errorf("read quarantined %s: %w", e.Name(), err)
		}
		tmp := filepath.Join(logs, ".tmp-"+e.Name())
		if err := os.WriteFile(tmp, set.redact(raw), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, filepath.Join(logs, e.Name())); err != nil {
			return fmt.Errorf("publish %s: %w", e.Name(), err)
		}
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("remove quarantine %s: %w", quarantine, err)
	}
	return nil
}

// adoptionSensitive rebuilds an attempt's redaction set after its worker died
// holding it (spec §3). The in-memory set died with the process, so adoption
// may publish only what it can PROVE it scanned completely: every input here
// comes from state captured before the conclusion (Task 10's reconciliation),
// and anything missing means this attempt's logs and artifacts stay withheld.
//
// ok=false is that withholding. It is the same shape as an unscannable
// read-back: no complete scan exists, so nothing workspace-derived publishes.
func adoptionSensitive(rec credentialReconciliation) (*sensitiveSet, bool) {
	if !rec.Bound {
		// No credential ever reached this attempt — the manifest write precedes
		// every container it could have been adopted from — so the empty set is
		// the complete set.
		return newSensitiveSet(), true
	}
	if !rec.HasManifest {
		return nil, false // nothing names what this attempt was handed
	}
	if !rec.StoredOK {
		// A mid-attempt operator rotation: the store cannot give the delivered
		// bytes back, and they are what the container actually saw.
		return nil, false
	}
	d, err := driverFor(rec.Manifest.HarnessKind)
	if err != nil {
		return nil, false // no per-kind field list, no complete scan
	}
	set := newSensitiveSet([]byte(rec.Manifest.BrokerToken), rec.Stored)
	if rec.Manifest.Custody == custodyMaterialized && d.sensitive != nil {
		// Materialized only: a brokered binding's credential is a raw key, and
		// the driver's extractor describes an auth file (Task 6's per-kind rule).
		sens, err := d.sensitive(rec.Stored)
		if err != nil {
			return nil, false
		}
		set.add(sens...)
	}
	set.add(rec.Conc.RefreshedSensitive...)
	return set, true
}
