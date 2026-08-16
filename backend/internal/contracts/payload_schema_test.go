package contracts

import (
	"encoding/json"
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// payloadSchemaNames pairs an artifact type with the component the generator
// publishes for it. The map is what makes this test address both directions:
// the key indexes runs.RequiredPayloadKeys, the value indexes the document.
//
// It is a SECOND hand-written list of the constrained artifact types, so the
// test asserts it covers runs.RequiredPayloadKeys exactly. Without that, a
// third constrained type added to the validator would simply not be looked at
// here — no failure, no published schema, and the drift this whole file exists
// to catch happening in the one place nothing watches.
var payloadSchemaNames = map[string]string{
	"plan":           "ApiPlanPayload",
	"review_verdict": "ApiReviewVerdictPayload",
}

// nullableRefKeys are keys the VALIDATOR requires that the CONTRACT
// deliberately does not mark required, and there is exactly one.
//
// review_verdict's `tests` is required and nullable: null means "this harness
// did not report test results", which the approval surface renders as unknown
// rather than as a tick, and an absent key would be indistinguishable from a
// harness that forgot. But its schema is a $ref, and this generator gives a
// $ref property no null branch — so `required` would render
// `tests: ApiTestOutcome` in TypeScript, non-optional AND non-nullable, on the
// one key whose whole purpose is that null is a legal value. Optional is the
// only shape this toolchain can express that makes a client narrow before it
// reads; the same trade is recorded on ApiPhaseResponse.evidence and
// ApiRunResponse.outcome in slice_test.go.
//
// The exception is pinned by name, not waved through: the loop below asserts
// the property EXISTS and is NOT required, so adding `required:"true"` back to
// payload_dto.go fails here rather than quietly lying to the client.
var nullableRefKeys = map[string]map[string]bool{
	"review_verdict": {"tests": true},
}

// TestPayloadSchemasMatchTheValidator is the anti-drift guard. Two independent
// descriptions of one shape exist by necessity — a Go struct the generator
// reflects, and a hand-written runtime validator — and the failure mode is
// silent: the client renders against one and the runner enforces the other,
// and a run fails for a field the UI never showed.
//
// It holds in BOTH directions. A key added to runs.RequiredPayloadKeys with no
// matching `required:"true"` fails the first loop; a `required:"true"` added to
// a field the validator does not require fails the count.
func TestPayloadSchemasMatchTheValidator(t *testing.T) {
	raw, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// Every constrained artifact type has a published schema, and this file
	// names no type the validator does not constrain. Both halves matter: the
	// first is what stops a new entry escaping the tie in silence, the second
	// is what stops this map outliving a type the validator dropped.
	for artifactType := range runs.RequiredPayloadKeys {
		if _, ok := payloadSchemaNames[artifactType]; !ok {
			t.Errorf("the validator constrains %q and no schema is published for it; "+
				"add it to payloadSchemaNames and to contracts' extraSchemas", artifactType)
		}
	}
	if len(payloadSchemaNames) != len(runs.RequiredPayloadKeys) {
		t.Errorf("payloadSchemaNames covers %d types, the validator constrains %d", len(payloadSchemaNames), len(runs.RequiredPayloadKeys))
	}

	for artifactType, schemaName := range payloadSchemaNames {
		schema, ok := doc.Components.Schemas[schemaName]
		if !ok {
			t.Fatalf("%s is not in the document — the payload contract is not published", schemaName)
		}
		required := map[string]bool{}
		for _, r := range schema.Required {
			required[r] = true
		}
		wantRequired := 0
		for _, want := range runs.RequiredPayloadKeys[artifactType] {
			if _, ok := schema.Properties[want]; !ok {
				t.Errorf("%s: the validator requires %q and the contract has no such property at all", schemaName, want)
				continue
			}
			if nullableRefKeys[artifactType][want] {
				// The exemption's whole justification is that the property's
				// schema is a $ref, which this generator gives no null branch.
				// A SCALAR listed here would be exempted for a reason that does
				// not apply to it — a scalar really does render ["string","null"]
				// while staying required, so it should simply be required.
				var prop struct {
					Ref string `json:"$ref"`
				}
				if err := json.Unmarshal(schema.Properties[want], &prop); err != nil {
					t.Errorf("%s: property %q: %v", schemaName, want, err)
				} else if prop.Ref == "" {
					t.Errorf("%s: property %q is exempted as a nullable $ref but its schema is %s — "+
						"a non-$ref property can be required AND nullable, so this exemption is unearned",
						schemaName, want, schema.Properties[want])
				}
				if required[want] {
					t.Errorf("%s: property %q is required, but the server sends null for it; "+
						"openapi-typescript renders a required $ref as non-nullable", schemaName, want)
				}
				continue
			}
			wantRequired++
			if !required[want] {
				t.Errorf("%s: the validator requires %q and the contract does not", schemaName, want)
			}
		}
		if len(schema.Required) != wantRequired {
			t.Errorf("%s: contract requires %v, validator requires %v (less the nullable-$ref exceptions %v)",
				schemaName, schema.Required, runs.RequiredPayloadKeys[artifactType], nullableRefKeys[artifactType])
		}
	}
}
