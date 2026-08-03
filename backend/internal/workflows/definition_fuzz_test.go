package workflows_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/workflows"
)

// FuzzParseDocument drives the whole document pipeline: any input either
// fails to parse with a classified error, or parses into a document whose
// canonical encoding is a fixed point with a stable hash and whose
// validation terminates without panicking. The f.Add seeds are the
// committed corpus (spec §7).
func FuzzParseDocument(f *testing.F) {
	f.Add([]byte(validDoc))
	f.Add([]byte(`{"schema": 1, "nodes": [{"id": "a", "type": "plan", "harness": "cc", "instructions": "x"}]}`))
	f.Add([]byte(`{"schema": 1, "nodes": []}`))
	f.Add([]byte(`{"schema": 2, "nodes": []}`))
	f.Add([]byte(`nope`))
	f.Add([]byte(`{}`))
	// Self-edge on a gate: shape-invalid but parseable.
	f.Add([]byte(`{"schema": 1, "nodes": [{"id": "g", "type": "gate"}], "edges": [{"from": "g", "to": "g", "artifact": "patch"}]}`))
	// A 2-cycle between implements: parseable, shape-valid, cyclic.
	f.Add([]byte(`{"schema": 1, "nodes": [{"id": "a", "type": "implement", "harness": "h", "instructions": "x"}, {"id": "b", "type": "implement", "harness": "h", "instructions": "x"}], "edges": [{"from": "a", "to": "b", "artifact": "patch"}, {"from": "b", "to": "a", "artifact": "patch"}]}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		d, err := workflows.ParseDocument(raw)
		if err != nil {
			return // classified rejection is a correct outcome
		}
		re, err := workflows.ParseDocument(d.Canonical())
		if err != nil {
			t.Fatalf("canonical bytes failed to reparse: %v", err)
		}
		if re.Hash() != d.Hash() {
			t.Fatalf("hash unstable across canonical round trip: %s vs %s", d.Hash(), re.Hash())
		}
		_ = workflows.Validate(d) // must terminate, never panic
	})
}
