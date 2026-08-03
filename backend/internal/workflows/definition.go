// Package workflows is the definitions bounded context (spec §2): the
// typed workflow-definition document — a DAG of typed steps with gates as
// first-class nodes — its validator, and immutable content-hashed
// versions behind the single write door. The document layer is pure:
// no database, no registry lookups; a definition is portable content
// (decision 5 in the plan header), and every projection — UI outline,
// future canvas/code views, the assistant — derives from these types.
package workflows

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// SchemaVersion is the only document schema this binary understands.
// Bump it (and branch in ParseDocument) when the format evolves.
const SchemaVersion = 1

// maxDocumentLen caps parsing before any allocation-heavy decode — the
// API body cap (1 MiB) restated here so the parser is safe standalone.
const maxDocumentLen = 1 << 20

// NodeType is the V1 step vocabulary (spec §1: plan / implement /
// review / gate / git-publish).
type NodeType string

const (
	NodePlan       NodeType = "plan"
	NodeImplement  NodeType = "implement"
	NodeReview     NodeType = "review"
	NodeGate       NodeType = "gate"
	NodeGitPublish NodeType = "git-publish"
)

func (t NodeType) Valid() bool {
	switch t {
	case NodePlan, NodeImplement, NodeReview, NodeGate, NodeGitPublish:
		return true
	}
	return false
}

// Agentic reports whether the node runs a harness (needs harness +
// instructions config).
func (t NodeType) Agentic() bool {
	return t == NodePlan || t == NodeImplement || t == NodeReview
}

// ArtifactType is a bindable work-product type (spec §2). log_ref and
// failure_snapshot are runtime outputs, never edge bindings (plan
// header, decision 8).
type ArtifactType string

const (
	ArtifactPlan          ArtifactType = "plan"
	ArtifactPatch         ArtifactType = "patch"
	ArtifactReviewVerdict ArtifactType = "review_verdict"
)

func (a ArtifactType) Valid() bool {
	return a == ArtifactPlan || a == ArtifactPatch || a == ArtifactReviewVerdict
}

// RevisionTransition is a gate's bounded on_request_changes loop
// (spec §2): re-run from Target, at most MaxRounds times. It is not a
// graph edge — the dataflow DAG stays acyclic.
type RevisionTransition struct {
	Target    string `json:"target"`
	MaxRounds int    `json:"max_rounds"`
}

// Node is one DAG node. Harness/Instructions are set on agentic types
// only; OnRequestChanges on gates only — the validator enforces both.
type Node struct {
	ID               string              `json:"id"`
	Type             NodeType            `json:"type"`
	Harness          string              `json:"harness,omitempty"`
	Instructions     string              `json:"instructions,omitempty"`
	OnRequestChanges *RevisionTransition `json:"on_request_changes,omitempty"`
}

// Edge is one typed artifact binding: To consumes Artifact produced by
// From (spec §2: edges carry typed artifact bindings).
type Edge struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Artifact ArtifactType `json:"artifact"`
}

// Document is the workflow-definition document — the canonical artifact
// every projection derives from. Field order here IS the canonical
// encoding order; never reorder these fields.
type Document struct {
	Schema int    `json:"schema"`
	Nodes  []Node `json:"nodes"`
	Edges  []Edge `json:"edges,omitempty"`
}

// ParseDocument strictly decodes raw: unknown fields rejected, trailing
// data rejected, schema version checked, empty edges normalized to nil
// so that "edges": [] and an absent key canonicalize identically.
func ParseDocument(raw []byte) (Document, error) {
	if len(raw) > maxDocumentLen {
		return Document{}, xerrors.E(xerrors.ClassInvalid, "definition document too large").
			WithFields(xerrors.FieldError{Field: "document", Message: fmt.Sprintf("must be at most %d bytes", maxDocumentLen)})
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Document
	if err := dec.Decode(&d); err != nil {
		return Document{}, xerrors.Wrap(xerrors.ClassInvalid, "invalid definition document", err)
	}
	if dec.More() {
		return Document{}, xerrors.E(xerrors.ClassInvalid, "invalid definition document: trailing data")
	}
	if d.Schema != SchemaVersion {
		return Document{}, xerrors.E(xerrors.ClassInvalid, "unsupported schema version").
			WithFields(xerrors.FieldError{Field: "schema", Message: fmt.Sprintf("must be %d", SchemaVersion)})
	}
	if len(d.Edges) == 0 {
		d.Edges = nil
	}
	return d, nil
}

// Canonical is the canonical byte encoding: compact JSON in struct field
// order. The content hash is computed over exactly these bytes, and
// ParseDocument(Canonical()) is a fixed point (fuzz-checked in Task 6).
func (d Document) Canonical() []byte {
	b, err := json.Marshal(d)
	if err != nil {
		// Document holds only marshalable types; this is unreachable.
		panic(fmt.Sprintf("workflows: canonical marshal: %v", err))
	}
	return b
}

// Hash is the immutable version identity (spec §2: content-hashed):
// lowercase-hex SHA-256 of the canonical bytes.
func (d Document) Hash() string {
	sum := sha256.Sum256(d.Canonical())
	return hex.EncodeToString(sum[:])
}
