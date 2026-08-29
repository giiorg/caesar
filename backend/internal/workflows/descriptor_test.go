package workflows

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The descriptor's two proofs, in opposite directions.
//
//	rows -> engine (SOUNDNESS):    every declared row is put to Validate, which
//	                               must agree with it. Catches a row that lies.
//	engine -> rows (COMPLETENESS): every (field, type) cell of the engine's own
//	                               vocabulary must HAVE a row. Catches a field
//	                               the engine grew and the descriptor did not.
//
// The second direction is the one that makes the §10.9 claim — "a Go-side rule
// change breaks CI until the descriptor and the editor both learn it" — a fact
// about a test. Every assertion iterating only Descriptor() is blind to a new
// field by construction: there is no row to iterate.

// nodeIndex is where conveyor() puts each type. conveyor holds exactly one node
// of each of the five types, which is why it can serve every row.
func nodeIndex(t *testing.T, typ NodeType) int {
	t.Helper()
	for i, n := range conveyor().Nodes {
		if n.Type == typ {
			return i
		}
	}
	t.Fatalf("conveyor has no %s node; the fixture no longer covers the vocabulary", typ)
	return -1
}

// violationAt reports whether some violation addresses exactly path.
func violationAt(vs []Violation, path string) bool {
	return slices.ContainsFunc(vs, func(v Violation) bool { return v.Path == path })
}

func pathsOf(vs []Violation) string {
	ps := make([]string, len(vs))
	for i, v := range vs {
		ps[i] = v.Path + ": " + v.Message
	}
	return "[" + strings.Join(ps, " | ") + "]"
}

// setWireField writes a value into the node's field named by a WIRE name. It
// switches on the wire name so that a field added to Node with no case here
// fails loudly rather than being silently skipped by every row that names it.
func setWireField(t *testing.T, d *Document, i int, wire string, present bool) {
	t.Helper()
	n := &d.Nodes[i]
	switch wire {
	case "harness":
		if present {
			n.Harness = "cc"
		} else {
			n.Harness = ""
		}
	case "instructions":
		if present {
			n.Instructions = "Do the thing."
		} else {
			n.Instructions = ""
		}
	case "on_request_changes":
		if present {
			// Target "impl" is agentic and dominates every node in the
			// conveyor, so this is legal wherever it is legal at all — the
			// violation a forbidden row expects is about the FIELD, never
			// about the target.
			n.OnRequestChanges = &RevisionTransition{Target: "impl", MaxRounds: 3}
		} else {
			n.OnRequestChanges = nil
		}
	default:
		t.Fatalf("no setter for wire field %q: Node grew a field and this helper did not learn it", wire)
	}
}

// TestDescriptorRowsAreProvenAgainstValidate is the soundness direction.
func TestDescriptorRowsAreProvenAgainstValidate(t *testing.T) {
	for _, r := range Descriptor() {
		t.Run(fmt.Sprintf("%s/%s/%s", r.NodeType, r.WireField, r.Requirement), func(t *testing.T) {
			i := nodeIndex(t, r.NodeType)
			path := fmt.Sprintf("nodes[%d].%s", i, r.WireField)

			switch r.Requirement {
			case RequirementRequired:
				missing := conveyor()
				setWireField(t, &missing, i, r.WireField, false)
				if vs := Validate(missing); !violationAt(vs, path) {
					t.Fatalf("row says %s is required on %s, but removing it drew no violation at %s: %s",
						r.WireField, r.NodeType, path, pathsOf(vs))
				}
				present := conveyor()
				setWireField(t, &present, i, r.WireField, true)
				if vs := Validate(present); violationAt(vs, path) {
					t.Fatalf("a conforming %s was still rejected at %s: %s", r.NodeType, path, pathsOf(vs))
				}

			case RequirementForbidden:
				with := conveyor()
				setWireField(t, &with, i, r.WireField, true)
				if vs := Validate(with); !violationAt(vs, path) {
					t.Fatalf("row says %s is forbidden on %s, but setting it drew no violation at %s: %s",
						r.WireField, r.NodeType, path, pathsOf(vs))
				}
				without := conveyor()
				setWireField(t, &without, i, r.WireField, false)
				if vs := Validate(without); violationAt(vs, path) {
					t.Fatalf("omitting a forbidden %s still drew a violation at %s: %s", r.WireField, path, pathsOf(vs))
				}

			case RequirementOptional:
				// Optional means BOTH shapes validate — which is a real
				// assertion, not a skip: it convicts a row that calls a field
				// optional when the engine in fact demands or refuses it.
				for _, present := range []bool{false, true} {
					d := conveyor()
					setWireField(t, &d, i, r.WireField, present)
					if vs := Validate(d); violationAt(vs, path) {
						t.Fatalf("row says %s is optional on %s, but present=%v drew a violation at %s: %s",
							r.WireField, r.NodeType, present, path, pathsOf(vs))
					}
				}

			default:
				t.Fatalf("unknown requirement %q", r.Requirement)
			}
		})
	}
}

// TestDescriptorByteCapsAreProvenAtTheBoundary walks each MaxBytes row to the
// edge: exactly max passes, max+1 fails. One case per row is built from a
// MULTIBYTE rune, because Validate measures len() — bytes — and a cap that had
// drifted to counting runes would pass an ASCII-only boundary test.
func TestDescriptorByteCapsAreProvenAtTheBoundary(t *testing.T) {
	for _, r := range Descriptor() {
		if r.MaxBytes == 0 {
			continue
		}
		t.Run(fmt.Sprintf("%s/%s", r.NodeType, r.WireField), func(t *testing.T) {
			i := nodeIndex(t, r.NodeType)
			path := fmt.Sprintf("nodes[%d].%s", i, r.WireField)

			// "é" is two bytes and one rune. A cap counting runes would let
			// atMax+1 through: it is max-1 runes long.
			ascii := func(n int) string { return strings.Repeat("a", n) }
			multi := func(n int) string { return strings.Repeat("a", n-2) + "é" }

			for name, build := range map[string]func(int) string{"ascii": ascii, "multibyte": multi} {
				t.Run(name, func(t *testing.T) {
					atMax := conveyor()
					write(t, &atMax.Nodes[i], r.WireField, build(r.MaxBytes))
					if got := len(build(r.MaxBytes)); got != r.MaxBytes {
						t.Fatalf("fixture is %d bytes, wanted exactly %d", got, r.MaxBytes)
					}
					if vs := Validate(atMax); violationAt(vs, path) {
						t.Fatalf("exactly %d bytes was rejected at %s: %s", r.MaxBytes, path, pathsOf(vs))
					}
					over := conveyor()
					write(t, &over.Nodes[i], r.WireField, build(r.MaxBytes+1))
					if vs := Validate(over); !violationAt(vs, path) {
						t.Fatalf("%d bytes (cap %d) drew no violation at %s: %s",
							r.MaxBytes+1, r.MaxBytes, path, pathsOf(vs))
					}
				})
			}
		})
	}
}

// write sets a string-valued wire field. Separate from setWireField because
// that one deals in presence and this one in a specific value.
func write(t *testing.T, n *Node, wire, value string) {
	t.Helper()
	switch wire {
	case "harness":
		n.Harness = value
	case "instructions":
		n.Instructions = value
	default:
		t.Fatalf("wire field %q carries no byte cap", wire)
	}
}

// TestDescriptorIntBoundsAreProvenAtAllFourCorners walks each MinInt/MaxInt row
// through min-1, min, max, max+1 — the four documents that pin a bound. Two
// would pin only that SOME bound exists.
func TestDescriptorIntBoundsAreProvenAtAllFourCorners(t *testing.T) {
	for _, r := range Descriptor() {
		if r.MinInt == nil && r.MaxInt == nil {
			continue
		}
		if r.MinInt == nil || r.MaxInt == nil {
			t.Fatalf("row %s/%s declares one half of a range; the four-corner proof needs both", r.NodeType, r.WireField)
		}
		if r.SubField == "" {
			t.Fatalf("row %s/%s declares int bounds with no sub_field to address them", r.NodeType, r.WireField)
		}
		t.Run(fmt.Sprintf("%s/%s.%s", r.NodeType, r.WireField, r.SubField), func(t *testing.T) {
			i := nodeIndex(t, r.NodeType)
			path := fmt.Sprintf("nodes[%d].%s.%s", i, r.WireField, r.SubField)

			for _, c := range []struct {
				rounds   int
				rejected bool
				what     string
			}{
				{*r.MinInt - 1, true, "min-1"},
				{*r.MinInt, false, "min"},
				{*r.MaxInt, false, "max"},
				{*r.MaxInt + 1, true, "max+1"},
			} {
				d := conveyor()
				d.Nodes[i].OnRequestChanges = &RevisionTransition{Target: "impl", MaxRounds: c.rounds}
				vs := Validate(d)
				if got := violationAt(vs, path); got != c.rejected {
					t.Fatalf("%s (max_rounds=%d): violation at %s = %v, want %v: %s",
						c.what, c.rounds, path, got, c.rejected, pathsOf(vs))
				}
			}
		})
	}
}

// TestEveryEngineFieldAndTypeHasARule is the completeness direction: the grid of
// the engine's OWN vocabulary, every cell of which must be described.
//
// This is the test that goes red when definition.Node grows a field. Nothing
// that iterates Descriptor() can do that — a field with no row produces no row
// to iterate.
func TestEveryEngineFieldAndTypeHasARule(t *testing.T) {
	rules := Descriptor()
	covered := make(map[string]int, len(rules))
	for _, r := range rules {
		covered[string(r.NodeType)+"\x00"+r.WireField]++
	}

	for _, f := range NodeFields() {
		if structuralNodeFields[f] {
			continue
		}
		for _, typ := range NodeTypes() {
			switch n := covered[string(typ)+"\x00"+f]; {
			case n == 0:
				t.Errorf("no FieldRule for (%s, %s): the engine has this field and the descriptor does not describe it", typ, f)
			case n > 1:
				t.Errorf("%d FieldRules for (%s, %s); a cell must be described exactly once", n, typ, f)
			}
		}
	}
}

// TestDescriptorDescribesNothingTheEngineLacks is the same grid read the other
// way: a row naming a field or type that does not exist describes nothing, and
// would sit in the table looking authoritative.
func TestDescriptorDescribesNothingTheEngineLacks(t *testing.T) {
	fields, types := NodeFields(), NodeTypes()
	for _, r := range Descriptor() {
		if !slices.Contains(fields, r.WireField) {
			t.Errorf("row (%s, %s) names a field definition.Node does not have", r.NodeType, r.WireField)
		}
		if structuralNodeFields[r.WireField] {
			t.Errorf("row (%s, %s) describes a structural field; those carry no per-type rule", r.NodeType, r.WireField)
		}
		if !slices.Contains(types, r.NodeType) {
			t.Errorf("row (%s, %s) names a type that is not in the vocabulary", r.NodeType, r.WireField)
		}
	}
}

// TestBuildDescriptorCarriesTheMechanicalVocabulary pins the emitted document's
// non-rule half to its sources rather than to a transcription.
func TestBuildDescriptorCarriesTheMechanicalVocabulary(t *testing.T) {
	doc := BuildDescriptor()

	if !slices.Equal(doc.NodeTypes, NodeTypes()) {
		t.Errorf("node_types = %v, want %v", doc.NodeTypes, NodeTypes())
	}
	if !slices.Equal(doc.NodeFields, NodeFields()) {
		t.Errorf("node_fields = %v, want %v", doc.NodeFields, NodeFields())
	}
	for _, typ := range NodeTypes() {
		if got, want := slices.Contains(doc.Agentic, typ), typ.Agentic(); got != want {
			t.Errorf("agentic contains %s = %v, want %v", typ, got, want)
		}
	}
	// The pattern the editor's rename validates with, character for character.
	// web/src/workflows/descriptor-coverage.test.ts compares this string to
	// NODE_ID_PATTERN.source.
	if want := `^[a-z][a-z0-9_-]{0,63}$`; doc.IDPattern != want {
		t.Errorf("id_pattern = %q, want %q", doc.IDPattern, want)
	}
	// A node id at the pattern's stated limit must actually validate: the
	// pattern is published to the editor as a promise about what Validate
	// accepts, not merely as the regexp this package happens to hold.
	d := conveyor()
	d.Nodes[0].ID = "p" + strings.Repeat("x", 63)
	for i := range d.Edges {
		if d.Edges[i].From == "plan" {
			d.Edges[i].From = d.Nodes[0].ID
		}
	}
	if vs := Validate(d); violationAt(vs, "nodes[0].id") {
		t.Errorf("a 64-character id the published pattern admits was rejected: %s", pathsOf(vs))
	}
}
