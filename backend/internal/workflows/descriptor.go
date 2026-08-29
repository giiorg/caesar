package workflows

import (
	"reflect"
	"strings"
)

// The §10.9 descriptor: the machine-readable statement of what Validate
// accepts, per node type and per field.
//
// It exists because the editor's forms are HAND-AUTHORED (locked outline B
// asked for "fields generated from the engine's schema"; what ships is
// hand-authored forms plus an exhaustive drift gate). Nothing generates a
// control from this file. What it does instead is make the two sides
// convictable: descriptor_test.go proves every row against Validate, and
// web/src/workflows/descriptor-coverage.test.ts holds CARD_REGISTRY to the
// emitted JSON. A rule that changes in validate.go and nowhere else fails one
// of those two.
//
// WHAT IS EMITTED VS WHAT IS DECLARED. The vocabulary is mechanical: which
// types exist (NodeTypes, definition.go), which are agentic (NodeType.Agentic),
// which fields a node HAS (NodeFields below, by reflection). The rules are
// hand-written, because they live in validate.go's branches and no reflection
// recovers them — so every one is CONVICTED against Validate rather than
// trusted. That split is the whole design: nothing here is believed on its own
// authority.

// FieldRule is one PROVEN statement about what Validate accepts.
//
// Each row is put to Validate twice by descriptor_test.go — once with a
// document that breaks it (a violation must come back, at this exact Path) and
// once with one that honours it (no violation on that path). A row nobody can
// convict is a row that has stopped describing the engine.
type FieldRule struct {
	NodeType NodeType `json:"node_type"`
	// WireField is the JSON name on definition.go's Node — harness,
	// instructions, on_request_changes. It is the join key against the
	// mechanically enumerated vocabulary, so it must be the WIRE name and
	// never an editor-side label: the editor calls the gate's field
	// "revision", and CardSpec carries that mapping rather than this file
	// pretending the engine has a field by that name.
	WireField   string `json:"wire_field"`
	Requirement string `json:"requirement"` // required | forbidden | optional
	// MaxBytes is the cap Validate applies with len() — BYTES, not runes, and
	// descriptor_test.go proves the distinction with a multibyte case.
	MaxBytes int `json:"max_bytes,omitempty"`
	// MinInt/MaxInt are for rules Validate expresses as an integer range
	// rather than a byte cap — on_request_changes.max_rounds is 1..10
	// (validate.go's maxRevisionRounds). They are pointers so that a bound of
	// zero is expressible and distinguishable from "no bound".
	MinInt *int `json:"min_int,omitempty"`
	MaxInt *int `json:"max_int,omitempty"`
	// SubField names the key INSIDE WireField the numeric bounds govern, and
	// is the tail of the violation Path — nodes[i].on_request_changes.max_rounds.
	SubField string `json:"sub_field,omitempty"`
}

// Requirement values. Strings rather than a Go enum because they cross to
// TypeScript through the JSON and are compared there as strings.
const (
	RequirementRequired  = "required"
	RequirementForbidden = "forbidden"
	RequirementOptional  = "optional"
)

// structuralNodeFields are the two fields every node carries by being a node at
// all. They are exempt from the completeness grid because "required on every
// type" is not a per-type rule and there is nothing for a card to render: the
// id is the card's identity and the type is which card it is.
//
// This is the ONE hand-maintained set in this file, and it is deliberately a
// var rather than an inline condition so that exempting a third field is a
// visible edit to a named thing — that being the only way to dodge the
// completeness test.
var structuralNodeFields = map[string]bool{"id": true, "type": true}

// NodeFields is the wire vocabulary of Node: its json tag names, in struct
// field order.
//
// REFLECTED, NOT LISTED. This is the direction that catches an ADDED engine
// field. Give Node a Timeout int with a json:"timeout" tag and this function
// reports it immediately, so the completeness half of
// descriptor_test.go goes red before anyone has written a rule for it — which
// is what makes "a Go-side change breaks CI until the descriptor and the editor
// both learn it" a fact about a test rather than a claim in a comment.
//
// descriptorgen and descriptor_test.go both read this one function, for the
// same reason both the corpus and this file read NodeTypes(): a second
// hand-written list is how a vocabulary and its gate drift apart.
func NodeFields() []string {
	t := reflect.TypeOf(Node{})
	out := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// intPtr keeps the rule table below readable; &1 is not an expression in Go.
func intPtr(v int) *int { return &v }

// Descriptor is the rule table: every (node type, non-structural field) pair,
// exactly once.
//
// The grid is exhaustive BY CONSTRUCTION — built by ranging NodeTypes() rather
// than by writing fifteen literals — so a sixth node type arrives here with
// rows already, and the completeness test's job is to catch the opposite case:
// a new FIELD, which no loop over types can invent a rule for.
func Descriptor() []FieldRule {
	var rules []FieldRule
	for _, t := range NodeTypes() {
		harness, instructions := FieldRule{
			NodeType: t, WireField: "harness", Requirement: RequirementForbidden,
		}, FieldRule{
			NodeType: t, WireField: "instructions", Requirement: RequirementForbidden,
		}
		if t.Agentic() {
			// validate.go: required and non-empty on agentic nodes, capped by
			// maxHarnessLen / maxInstructionsLen.
			harness.Requirement, harness.MaxBytes = RequirementRequired, maxHarnessLen
			instructions.Requirement, instructions.MaxBytes = RequirementRequired, maxInstructionsLen
		}

		// validate.go: a revision transition is legal on a gate and nowhere
		// else, and is never required — a gate without one simply has no
		// revision loop.
		revision := FieldRule{
			NodeType: t, WireField: "on_request_changes", Requirement: RequirementForbidden,
		}
		if t == NodeGate {
			revision.Requirement = RequirementOptional
			revision.SubField = "max_rounds"
			revision.MinInt, revision.MaxInt = intPtr(minRevisionRounds), intPtr(maxRevisionRounds)
		}

		rules = append(rules, harness, instructions, revision)
	}
	return rules
}

// DescriptorDocument is the emitted artifact's shape: the mechanical vocabulary
// alongside the declared rules. Field order here is the JSON's key order, and
// the file is diffed byte-for-byte by contracts:check — do not reorder.
type DescriptorDocument struct {
	NodeTypes  []NodeType  `json:"node_types"`
	Agentic    []NodeType  `json:"agentic"`
	NodeFields []string    `json:"node_fields"`
	IDPattern  string      `json:"id_pattern"`
	Rules      []FieldRule `json:"rules"`
}

// BuildDescriptor assembles the document descriptorgen writes.
//
// IDPattern comes off the compiled regexp's own String(), not a copy of the
// literal: the web coverage test compares it to NODE_ID_PATTERN.source, and
// that comparison is only a check at all while each side has exactly one
// spelling of the pattern.
func BuildDescriptor() DescriptorDocument {
	types := NodeTypes()
	agentic := make([]NodeType, 0, len(types))
	for _, t := range types {
		if t.Agentic() {
			agentic = append(agentic, t)
		}
	}
	return DescriptorDocument{
		NodeTypes:  types,
		Agentic:    agentic,
		NodeFields: NodeFields(),
		IDPattern:  nodeIDPattern.String(),
		Rules:      Descriptor(),
	}
}
