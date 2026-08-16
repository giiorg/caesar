package runs

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// payloadSchemaVersion is the document schema version at which artifact
// payloads start being constrained. Below it, nothing is checked — see
// ValidateArtifactPayload.
const payloadSchemaVersion = 2

// RequiredPayloadKeys is the required set per constrained artifact type,
// EXPORTED so internal/contracts can hold it against the generated document's
// own `required` arrays. Two hand-maintained descriptions of one shape is the
// drift this export exists to make impossible.
var RequiredPayloadKeys = map[string][]string{
	ArtifactKindPlan:          {"summary", "steps"},
	ArtifactKindReviewVerdict: {"verdict", "findings", "tests"},
}

// The enum vocabularies, as sorted slices rather than sets: they are four
// entries at most, and a slice puts the allowed values in the error message in
// a fixed order without a sort at the failure site.
var (
	verdictValues  = []string{"approve", "request_changes", "reject"}
	severityValues = []string{"info", "minor", "major", "blocker"}
	testStatuses   = []string{"passed", "failed", "not_run"}
)

// ValidateArtifactPayload checks a declared payload against the shape its
// pinned document version promises.
//
// VALIDATION IS A PROPERTY OF THE DOCUMENT SCHEMA VERSION (spec §2.3). A
// schema-1 document is never validated, which is exactly today's behaviour; a
// run pinned its definition at intake and a deploy cannot retroactively change
// that pin, so every in-flight attempt is safe by construction and no drain is
// needed. The prompt the node was given and the validator that judges it read
// the SAME pin — the failure being avoided is a node instructed in one shape
// and judged against another.
//
// UNKNOWN KEYS ARE ALLOWED THROUGH, which is the opposite of the envelope's
// DisallowUnknownFields and is intentional: the envelope is written by our
// runner, payloads are written by an LLM harness, and a harness that adds a
// useful key must not fail a run.
//
// The required sets are deliberately minimal. A schema stricter than a model
// can reliably produce converts model variance into failed runs, and a
// validation failure here is a PERMANENT attempt failure.
//
// It is hand-written, and the reason is that this repo has no JSON Schema
// runtime: adding one to check two shapes would be a dependency with more
// surface than the thing it checks. The published CONTRACT is the generated
// document (runs/api/payload_dto.go), and contracts/payload_schema_test.go is
// what keeps the two descriptions from drifting.
func ValidateArtifactPayload(schemaVersion int, artifactType string, payload map[string]any) error {
	if schemaVersion < payloadSchemaVersion {
		return nil
	}
	switch artifactType {
	case ArtifactKindPlan:
		return validatePlanPayload(payload)
	case ArtifactKindReviewVerdict:
		return validateVerdictPayload(payload)
	default:
		// patch is extracted, log_ref and failure_snapshot are the runtime's own
		// observations, publication and gate_note are written by the control
		// plane. None is a harness's to shape.
		return nil
	}
}

func validatePlanPayload(p map[string]any) error {
	if err := requireString(p, "summary"); err != nil {
		return err
	}
	steps, err := requireArray(p, "steps")
	if err != nil {
		return err
	}
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			return payloadErr("steps[%d] is not an object", i)
		}
		if err := requireString(step, "title"); err != nil {
			return payloadErr("steps[%d]: %v", i, err)
		}
		if err := optionalString(step, "detail"); err != nil {
			return payloadErr("steps[%d]: %v", i, err)
		}
	}
	return nil
}

func validateVerdictPayload(p map[string]any) error {
	if _, err := requireEnum(p, "verdict", verdictValues); err != nil {
		return err
	}
	findings, err := requireArray(p, "findings")
	if err != nil {
		return err
	}
	for i, raw := range findings {
		f, ok := raw.(map[string]any)
		if !ok {
			return payloadErr("findings[%d] is not an object", i)
		}
		if _, err := requireEnum(f, "severity", severityValues); err != nil {
			return payloadErr("findings[%d]: %v", i, err)
		}
		if err := requireString(f, "message"); err != nil {
			return payloadErr("findings[%d]: %v", i, err)
		}
		if err := optionalString(f, "file"); err != nil {
			return payloadErr("findings[%d]: %v", i, err)
		}
		if err := optionalNumber(f, "line"); err != nil {
			return payloadErr("findings[%d]: %v", i, err)
		}
	}
	// tests is REQUIRED and NULLABLE. Null means "this harness did not report
	// test results", which the hard-facts row renders as unknown; an absent key
	// would be indistinguishable from a harness that forgot the field.
	raw, ok := p["tests"]
	if !ok {
		return payloadErr("tests is required; use null to say tests were not reported")
	}
	if raw == nil {
		return nil
	}
	tests, ok := raw.(map[string]any)
	if !ok {
		return payloadErr("tests is neither an object nor null")
	}
	if _, err := requireEnum(tests, "status", testStatuses); err != nil {
		return payloadErr("tests: %v", err)
	}
	for _, opt := range []string{"passed", "failed"} {
		if err := optionalNumber(tests, opt); err != nil {
			return payloadErr("tests: %v", err)
		}
	}
	if err := optionalString(tests, "details"); err != nil {
		return payloadErr("tests: %v", err)
	}
	return nil
}

// The five helpers below share one asymmetry, and it is what keeps "unknown
// keys pass" from collapsing into "anything passes": an OPTIONAL key may be
// absent or explicitly null, but a key that is PRESENT with the wrong type is
// a failure whether the schema required it or not.

func requireString(p map[string]any, key string) error {
	v, ok := p[key]
	if !ok {
		return payloadErr("%s is required", key)
	}
	if _, ok := v.(string); !ok {
		return payloadErr("%s is not a string", key)
	}
	return nil
}

func optionalString(p map[string]any, key string) error {
	v, ok := p[key]
	if !ok || v == nil {
		return nil
	}
	if _, ok := v.(string); !ok {
		return payloadErr("%s is not a string", key)
	}
	return nil
}

func requireArray(p map[string]any, key string) ([]any, error) {
	v, ok := p[key]
	if !ok {
		return nil, payloadErr("%s is required", key)
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, payloadErr("%s is not an array", key)
	}
	return arr, nil
}

func requireEnum(p map[string]any, key string, allowed []string) (string, error) {
	v, ok := p[key]
	if !ok {
		return "", payloadErr("%s is required", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", payloadErr("%s is not a string", key)
	}
	for _, a := range allowed {
		if s == a {
			return s, nil
		}
	}
	return "", payloadErr("%s is %q; want one of %s", key, s, strings.Join(allowed, ", "))
}

// optionalNumber accepts every Go shape a number reaches this validator in,
// and the list is longer than encoding/json's because not every caller decodes
// JSON first. A payload read off a harness's declared-artifacts file arrives
// as float64; one built in Go — the fake executor's, and any future control-
// plane producer — carries untyped int literals straight to the orchestrator
// with no marshal step between. A json.Number turns up wherever a decoder was
// configured with UseNumber. Accepting only float64 would reject a payload the
// system itself wrote.
func optionalNumber(p map[string]any, key string) error {
	v, ok := p[key]
	if !ok || v == nil {
		return nil
	}
	switch v.(type) {
	case int, int64, float64, json.Number:
		return nil
	}
	return payloadErr("%s is not a number", key)
}

func payloadErr(format string, args ...any) error {
	return xerrors.E(xerrors.ClassInvalid, fmt.Sprintf(format, args...))
}
