// Out-of-package, like every other _test.go in this directory: the validator
// and the artifact kinds it switches on are exported, and only the
// *_internal_test.go files here reach for unexported helpers.
package runs_test

import (
	"encoding/json"
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestValidateArtifactPayload(t *testing.T) {
	goodPlan := map[string]any{
		"summary": "Adds session revocation.",
		"steps":   []any{map[string]any{"title": "Add the table", "detail": "with an index"}},
	}
	goodVerdict := map[string]any{
		"verdict":  "approve",
		"findings": []any{},
		"tests":    map[string]any{"status": "passed", "passed": float64(12), "failed": float64(0)},
	}

	t.Run("a schema-1 document is never validated", func(t *testing.T) {
		// EXACTLY TODAY'S BEHAVIOUR, PRESERVED. Every run created before this
		// deploy pinned a v1 definition, and a deploy cannot retroactively
		// change a pin — which is the whole of the compatibility argument and
		// the reason no drain is needed.
		if err := runs.ValidateArtifactPayload(1, runs.ArtifactKindPlan, map[string]any{"content": "anything"}); err != nil {
			t.Errorf("schema 1 rejected a payload: %v", err)
		}
	})

	t.Run("accepts the minimum", func(t *testing.T) {
		if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindPlan, goodPlan); err != nil {
			t.Errorf("plan: %v", err)
		}
		if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindReviewVerdict, goodVerdict); err != nil {
			t.Errorf("review_verdict: %v", err)
		}
	})

	t.Run("UNKNOWN KEYS PASS", func(t *testing.T) {
		// The opposite of the envelope's DisallowUnknownFields, deliberately.
		// The envelope is written by our runner; a payload is written by an LLM
		// harness, and a harness that adds a useful key must not fail a run.
		p := map[string]any{"summary": "s", "steps": []any{}, "confidence": 0.8, "content": "legacy"}
		if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindPlan, p); err != nil {
			t.Errorf("an extra key failed the payload: %v", err)
		}
	})

	t.Run("a missing required key fails", func(t *testing.T) {
		if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindPlan, map[string]any{"summary": "s"}); err == nil {
			t.Error("a plan with no steps was accepted")
		}
		if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindReviewVerdict,
			map[string]any{"verdict": "approve", "findings": []any{}}); err == nil {
			t.Error("a verdict with no tests key was accepted")
		}
	})

	t.Run("tests says NOT REPORTED with an explicit null, and the key is still required", func(t *testing.T) {
		// "Not reported" must be a value, so the hard-facts row can render the
		// fact as unknown rather than inventing a tick. An ABSENT key would be
		// indistinguishable from a harness that forgot.
		ok := map[string]any{"verdict": "approve", "findings": []any{}, "tests": nil}
		if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindReviewVerdict, ok); err != nil {
			t.Errorf("tests: null was rejected: %v", err)
		}
	})

	t.Run("wrong types and out-of-range enums fail", func(t *testing.T) {
		for name, p := range map[string]map[string]any{
			"steps is not an array":   {"summary": "s", "steps": "one, two"},
			"a step is not an object": {"summary": "s", "steps": []any{"do it"}},
			"a step has no title":     {"summary": "s", "steps": []any{map[string]any{"detail": "d"}}},
			"summary is not a string": {"summary": 3, "steps": []any{}},
			// An OPTIONAL key that is present and wrong-typed is still a
			// failure. That asymmetry — absent is fine, present-and-wrong is
			// not — is the whole difference between "unknown keys pass" and
			// "anything passes", and nothing else in this table pins it.
			"a step detail is not a string": {"summary": "s", "steps": []any{map[string]any{"title": "t", "detail": 7}}},
		} {
			if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindPlan, p); err == nil {
				t.Errorf("plan accepted %s", name)
			}
		}
		for name, p := range map[string]map[string]any{
			"verdict is not in the enum":  {"verdict": "lgtm", "findings": []any{}, "tests": nil},
			"findings is not an array":    {"verdict": "approve", "findings": "none", "tests": nil},
			"a finding is not an object":  {"verdict": "approve", "findings": []any{"looks bad"}, "tests": nil},
			"a finding has no severity":   {"verdict": "approve", "findings": []any{map[string]any{"message": "m"}}, "tests": nil},
			"a finding severity is bogus": {"verdict": "approve", "findings": []any{map[string]any{"severity": "spicy", "message": "m"}}, "tests": nil},
			"a finding has no message":    {"verdict": "approve", "findings": []any{map[string]any{"severity": "minor"}}, "tests": nil},
			"a finding file is not a string": {"verdict": "approve", "tests": nil, "findings": []any{
				map[string]any{"severity": "minor", "message": "m", "file": 3},
			}},
			"a finding line is not a number": {"verdict": "approve", "tests": nil, "findings": []any{
				map[string]any{"severity": "minor", "message": "m", "line": "twelve"},
			}},
			"tests is neither an object nor null": {"verdict": "approve", "findings": []any{}, "tests": "passed"},
			"tests status is bogus":               {"verdict": "approve", "findings": []any{}, "tests": map[string]any{"status": "maybe"}},
			"tests has no status":                 {"verdict": "approve", "findings": []any{}, "tests": map[string]any{"passed": float64(3)}},
			"tests passed is not a number": {"verdict": "approve", "findings": []any{}, "tests": map[string]any{
				"status": "passed", "passed": "twelve",
			}},
			"tests details is not a string": {"verdict": "approve", "findings": []any{}, "tests": map[string]any{
				"status": "passed", "details": 12,
			}},
		} {
			if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindReviewVerdict, p); err == nil {
				t.Errorf("review_verdict accepted %s", name)
			}
		}
	})

	// A number reaches this validator in more than one Go shape and the
	// difference is not academic: a payload decoded from a harness's
	// declared-artifacts file arrives as float64 (encoding/json's default),
	// while the fake executor hands the orchestrator untyped Go int literals
	// with no marshal step in between. A float64-only type switch passes every
	// other subtest here and then rejects a payload the system itself wrote.
	t.Run("a number is accepted in every shape a caller can hand us", func(t *testing.T) {
		for name, line := range map[string]any{
			"an untyped Go int literal": 12,
			"an int64":                  int64(12),
			"a json.Decode float64":     float64(12),
			"a json.Number":             json.Number("12"),
		} {
			p := map[string]any{"verdict": "approve", "tests": nil, "findings": []any{
				map[string]any{"severity": "minor", "message": "m", "line": line},
			}}
			if err := runs.ValidateArtifactPayload(2, runs.ArtifactKindReviewVerdict, p); err != nil {
				t.Errorf("%s was rejected: %v", name, err)
			}
		}
	})

	t.Run("an unconstrained type is not validated", func(t *testing.T) {
		// patch, log_ref, failure_snapshot, publication and gate_note are the
		// control plane's own or extracted, not declared by a harness. This
		// function must not become a second, weaker check on them.
		for _, k := range []string{runs.ArtifactKindPatch, runs.ArtifactKindLogRef, runs.ArtifactKindPublication} {
			if err := runs.ValidateArtifactPayload(2, k, map[string]any{"whatever": true}); err != nil {
				t.Errorf("%s was validated: %v", k, err)
			}
		}
	})
}
