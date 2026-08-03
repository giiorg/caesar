# caesar Plan 04 — Workflow Definitions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The definitions slice of the domain (spec §2): the typed workflow-definition document, the validator enforcing every §2 structural invariant (typed artifact bindings, cycle detection, linear patch lineage, gate-dominates-exact-patch, one-gate-per-publish, revision-transition dominance), immutable content-hashed versions behind the single write door at `/api/v1/workflows`, and the start of property (`rapid`) + fuzz coverage per spec §7.

**Architecture:** `internal/workflows` is the third bounded context (peer of `identity` and `assets`): pure domain code (document types, canonicalization/hashing, graph machinery, validator) plus a service, sqlc store, and HTTP handlers. The validator is deliberately dependency-free — it validates *shape and structure only* and never touches the database (a definition is portable content; a harness reference is validated as a name, resolved against the registry at run creation in Plan 05). The write door is `POST /api/v1/workflows/{id}/versions`: parse strictly → validate → canonicalize → SHA-256 content hash → insert an immutable version row (idempotent on identical content). Auth reuses the Plan 03 `platform/authn` ports; only `SessionAuthenticator` is needed (no per-project grants — see the RBAC pin below).

**Tech Stack:** Everything from Plans 01–03 plus one new Go dependency: `pgregory.net/rapid` **v1.3.0** (property testing, spec §7 — MIT, zero transitive deps). Fuzzing is native `go test -fuzz` with a committed seed corpus.

## Decisions this plan bakes in

Flag these to sofo if any look wrong; they are interpretations where the spec left latitude:

1. **RBAC for workflows:** definitions are org-global library content, not project assets — the spec's per-project grant ladder doesn't apply. Pinned: **writes (create / update metadata / new version / delete) are admin-only; reads (list / get, versions) require any authenticated user** (members must browse workflows to create tasks). Loosening later is an additive change.
2. **Gates are pass-through nodes.** A gate consumes exactly one *examined* artifact (`plan` or `patch`) and its outgoing edges re-emit exactly that artifact type. This is how "the gate holds the patch until approval" (spec §2) becomes graph structure: `implement → (patch) → gate → (patch) → git-publish`, and "examines exactly the patch the publish consumes" becomes an origin-equality check through the pass-through chain.
3. **Linear patch lineage, formalized:** for every node, the set of patch-producing (`implement`) ancestors must be **totally ordered by the ancestor relation** (form a chain). This permits chained implements (`implement → implement`) and rejects any node whose ancestry merges two divergent patch lines (the spec's "more than one patch-producing ancestor", read as *incomparable* ancestors — a chain is one lineage).
4. **Revision-loop slices can never contain a `git-publish` node — guaranteed structurally, not by an extra check.** A slice is `{target, gate} ∪ (descendants(target) ∩ ancestors(gate))`; membership in `ancestors(gate)` requires an outgoing edge, and git-publish has none (it produces nothing). Re-running a forge effect inside a request-changes loop is therefore impossible by construction; no dead validator branch is written for it.
5. **Harness references are shape-checked only** (a trimmed 1–100-char name). Existence against the registry is resolved at run creation (Plan 05); definitions stay portable and their content hash never depends on mutable registry state.
6. **The write door is idempotent on content:** posting a document whose canonical bytes already exist for that workflow returns the existing version (200) instead of minting a duplicate (201 on creation). Version numbers are per-workflow, monotonic from 1.
7. **Property tests live behind a `property` build tag** (mirroring the `integration` tag): PR CI runs them with a pinned rapid seed (deterministic), the new nightly workflow runs them randomized — exactly the spec §7 split.
8. **`log_ref` and `failure_snapshot` are not bindable artifact types.** They are runtime outputs every attempt can produce (spec §2); definition edges bind only `plan`, `patch`, `review_verdict`.

## Global Constraints

Everything in Plans 01–03's Global Constraints still applies verbatim (module path `github.com/giiorg/caesar/backend`, layout law + arch tests, dependency deny-list, strict JSON config, stdlib asserts + go-cmp, slog JSON, TDD + table-driven + behavioral names, Taskfile parity, generated code never hand-edited, commit style — one short sentence, **no AI attribution of any kind**). New pins for this plan:

- **Document format (`schema: 1`, strict JSON — unknown fields rejected, trailing data rejected, 1 MiB parse cap):**

  ```json
  {
    "schema": 1,
    "nodes": [
      {"id": "plan", "type": "plan", "harness": "cc", "instructions": "Plan the change."},
      {"id": "impl", "type": "implement", "harness": "cc", "instructions": "Implement the plan."},
      {"id": "review", "type": "review", "harness": "cc", "instructions": "Review the patch."},
      {"id": "gate", "type": "gate", "on_request_changes": {"target": "impl", "max_rounds": 3}},
      {"id": "publish", "type": "git-publish"}
    ],
    "edges": [
      {"from": "plan", "to": "impl", "artifact": "plan"},
      {"from": "impl", "to": "review", "artifact": "patch"},
      {"from": "impl", "to": "gate", "artifact": "patch"},
      {"from": "review", "to": "gate", "artifact": "review_verdict"},
      {"from": "gate", "to": "publish", "artifact": "patch"}
    ]
  }
  ```

- **Node vocabulary (spec §1: repo-centric agentic development):** `plan`, `implement`, `review` (agentic — require `harness` + `instructions`), `gate` (decision — optional `on_request_changes`), `git-publish` (effect — no config). Artifact types: `plan`, `patch`, `review_verdict`.
- **Per-type binding signatures (consumed → produced), enforced by the validator:**

  | node type | consumes (by artifact, with multiplicity) | produces |
  |---|---|---|
  | `plan` | nothing | `plan` |
  | `implement` | `plan` ≤ 1 · `patch` ≤ 1 | `patch` |
  | `review` | `patch` = 1 | `review_verdict` |
  | `gate` | examined (`plan` or `patch`) = 1 · `review_verdict` ≤ 1 | pass-through of the examined type |
  | `git-publish` | `patch` = 1 | nothing (outgoing edges forbidden) |

- **Validation pins:** node ids match `^[a-z][a-z0-9_-]{0,63}$`, unique · 1–50 nodes · ≤ 200 edges · no self-edges, no duplicate `(from, to, artifact)` triples · `instructions` 1–8192 bytes · `harness` trimmed, 1–100 bytes · `max_rounds` 1–10, required when `on_request_changes` is present · `on_request_changes` only on gates; `harness`/`instructions` only on agentic nodes. Length counts are **bytes** (house convention, session-15 ruling).
- **Structural invariants (pinned formalizations — the heart of the plan):**
  1. **Acyclic:** Kahn's algorithm; a cycle is reported with its member nodes.
  2. **Typed bindings line up:** every edge's artifact is producible by its source (per the table; for gates, the examined type) and consumable by its target within multiplicity.
  3. **Linear patch lineage:** per node, the `implement` nodes in its proper ancestry are totally ordered by ancestry (decision 3 above).
  4. **Dominance** is computed with a virtual root over all in-degree-0 nodes: `dom(n) = {n} ∪ ⋂ dom(pred)` in topological order. For any node, its dominator set is totally ordered by dominance, so "nearest dominator" is well defined.
  5. **Revision transitions:** the target exists, is an agentic node, and **strictly dominates its gate**; the slices of distinct gates — slice = `{target, gate} ∪ (descendants(target) ∩ ancestors(gate))` — are **pairwise disjoint** (no nested or overlapping revision loops in V1). A slice containing a `git-publish` node is impossible by construction (decision 4).
  6. **Gate-dominates-exact-patch:** for every `git-publish` node P with patch input edge of origin X (origin = walk back through pass-through gates to the producing node), there is at least one *eligible* gate G: G strictly dominates P, G examines a `patch` whose origin is also X, and no `implement` node lies strictly between G and P (`descendants(G) ∩ ancestors(P)` contains no implement).
  7. **One gate per publish:** each publish is assigned its **nearest** eligible dominating gate; two publish nodes assigned the same gate are rejected (the ApprovedPatch authorization is single-use).
- **Canonicalization & content hash:** the canonical encoding is compact `json.Marshal` of the parsed `Document` struct (fixed field order; empty `edges` normalized to omitted). `Hash()` = lowercase-hex SHA-256 of the canonical bytes. Parse → Canonical is a fixed point (fuzz-checked). The DB stores the document as `jsonb` (Postgres re-normalizes byte order), so **the hash is always recomputed from the parsed struct, never from retrieved bytes** — an integration test pins retrieve → parse → hash == stored `content_hash`.
- **RBAC matrix:** create / update / delete workflow, create version = **admin** · list / get workflow, list / get versions = **any authenticated user**. Failures: 401 unauthenticated, 403 non-admin on writes.
- **Audit vocabulary bump (append to Plan 03's):** `workflow.created`, `workflow.updated`, `workflow.deleted`, `workflow.version_created`. All carry the acting admin as actor.
- **Validator errors at the API:** each violation becomes one `errors[]` field entry in the problem envelope — `field` = the violation path (`nodes[3].harness`, `edges[0]`, `nodes[4]`), `message` = the violation message. The editor UI (Plan 12) renders these directly.
- **Property/fuzz mechanics:** rapid pinned at **v1.3.0**; property files carry `//go:build property`; `task backend:test:property` runs them with `-rapid.seed={{.RAPID_SEED}}` (default `1` — pinned; `0` = randomized, nightly) and `-rapid.checks={{.RAPID_CHECKS}}` (default `100`). Every tag-aware gate (`vet`, `vuln`, `analyze`, golangci `build-tags`, arch `LoadGraph`) adds `property` next to `integration`. Fuzz targets are untagged — their seed corpus runs as ordinary tests; `task backend:fuzz` runs the fuzzing engine (nightly, `FUZZTIME` per target).
- **Contracts drift gate grows:** sqlc.yaml gains a fourth generation block (`internal/workflows/store`); `task contracts:check`'s `git diff` path list adds `backend/internal/workflows/store`. Every task that adds `httpx.Operation`s runs `task contracts:gen` and commits the regenerated `backend/api/openapi.json` + `web/src/api/schema.d.ts` in the same commit.
- All `go` / `task` commands run from the repo root unless the step says otherwise. Integration tests: `cd backend && go test -race -tags integration -count=1 ./...` (Docker required).

---

### Task 1: The definition document — types, strict parse, canonical bytes, content hash

The document layer is the single source of truth every projection derives from (spec §2). This task lands the Go types, the strict parser (unknown fields and trailing data rejected, spec §8 default-deny), the canonical encoding, and the SHA-256 content hash. No validation beyond schema-version and size yet — that is Tasks 2–4.

**Files:**
- Create: `backend/internal/workflows/definition.go`
- Create: `backend/internal/workflows/definition_test.go`

**Interfaces:**
- Consumes: `platform/xerrors` only.
- Produces (later tasks and later plans rely on these exact names):
  - `workflows.NodeType` (`NodePlan`, `NodeImplement`, `NodeReview`, `NodeGate`, `NodeGitPublish`; methods `Valid() bool`, `Agentic() bool`)
  - `workflows.ArtifactType` (`ArtifactPlan`, `ArtifactPatch`, `ArtifactReviewVerdict`; method `Valid() bool`)
  - `workflows.Node{ID string; Type NodeType; Harness string; Instructions string; OnRequestChanges *RevisionTransition}`
  - `workflows.RevisionTransition{Target string; MaxRounds int}`
  - `workflows.Edge{From, To string; Artifact ArtifactType}`
  - `workflows.Document{Schema int; Nodes []Node; Edges []Edge}`
  - `workflows.ParseDocument(raw []byte) (Document, error)` — classified `xerrors` on failure
  - `(Document).Canonical() []byte` and `(Document).Hash() string`
  - `workflows.SchemaVersion = 1`

- [ ] **Step 1: Write the failing tests**

`backend/internal/workflows/definition_test.go`:

```go
package workflows_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/workflows"
)

// validDoc is the canonical five-node conveyor from the plan header; the
// whole test suite reuses it.
const validDoc = `{
  "schema": 1,
  "nodes": [
    {"id": "plan", "type": "plan", "harness": "cc", "instructions": "Plan the change."},
    {"id": "impl", "type": "implement", "harness": "cc", "instructions": "Implement the plan."},
    {"id": "review", "type": "review", "harness": "cc", "instructions": "Review the patch."},
    {"id": "gate", "type": "gate", "on_request_changes": {"target": "impl", "max_rounds": 3}},
    {"id": "publish", "type": "git-publish"}
  ],
  "edges": [
    {"from": "plan", "to": "impl", "artifact": "plan"},
    {"from": "impl", "to": "review", "artifact": "patch"},
    {"from": "impl", "to": "gate", "artifact": "patch"},
    {"from": "review", "to": "gate", "artifact": "review_verdict"},
    {"from": "gate", "to": "publish", "artifact": "patch"}
  ]
}`

func TestParseDocument_AcceptsTheCanonicalConveyor(t *testing.T) {
	d, err := workflows.ParseDocument([]byte(validDoc))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if len(d.Nodes) != 5 || len(d.Edges) != 5 {
		t.Fatalf("got %d nodes, %d edges, want 5 and 5", len(d.Nodes), len(d.Edges))
	}
	if d.Nodes[3].OnRequestChanges == nil || d.Nodes[3].OnRequestChanges.Target != "impl" || d.Nodes[3].OnRequestChanges.MaxRounds != 3 {
		t.Fatalf("gate revision transition not parsed: %+v", d.Nodes[3].OnRequestChanges)
	}
}

func TestParseDocument_RejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name, raw, wantMsg string
	}{
		{"not json", `nope`, "invalid definition document"},
		{"unknown top-level field", `{"schema": 1, "nodes": [], "surprise": true}`, "invalid definition document"},
		{"unknown node field", `{"schema": 1, "nodes": [{"id": "a", "type": "plan", "prompt": "x"}]}`, "invalid definition document"},
		{"trailing data", `{"schema": 1, "nodes": []}{"schema": 1}`, "invalid definition document"},
		{"wrong schema version", `{"schema": 2, "nodes": []}`, "unsupported schema version"},
		{"missing schema version", `{"nodes": []}`, "unsupported schema version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := workflows.ParseDocument([]byte(c.raw))
			if err == nil {
				t.Fatal("ParseDocument accepted malformed input")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("error %q does not contain %q", err, c.wantMsg)
			}
		})
	}
}

func TestParseDocument_RejectsOversizedInput(t *testing.T) {
	raw := []byte(`{"schema": 1, "nodes": [], "x": "` + strings.Repeat("a", 1<<20) + `"}`)
	if _, err := workflows.ParseDocument(raw); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want size rejection, got %v", err)
	}
}

func TestCanonical_IsAParseFixedPoint(t *testing.T) {
	d, err := workflows.ParseDocument([]byte(validDoc))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := workflows.ParseDocument(d.Canonical())
	if err != nil {
		t.Fatalf("reparse of canonical bytes: %v", err)
	}
	if diff := cmp.Diff(d, d2); diff != "" {
		t.Fatalf("canonical round trip changed the document:\n%s", diff)
	}
	if string(d.Canonical()) != string(d2.Canonical()) {
		t.Fatal("canonical bytes are not stable")
	}
}

func TestHash_IgnoresFormattingButSeesContent(t *testing.T) {
	d1, err := workflows.ParseDocument([]byte(validDoc))
	if err != nil {
		t.Fatal(err)
	}
	// Same content, different whitespace and key order.
	reordered := `{"nodes": [
    {"type": "plan", "instructions": "Plan the change.", "id": "plan", "harness": "cc"},
    {"id": "impl", "type": "implement", "harness": "cc", "instructions": "Implement the plan."},
    {"id": "review", "type": "review", "harness": "cc", "instructions": "Review the patch."},
    {"id": "gate", "type": "gate", "on_request_changes": {"max_rounds": 3, "target": "impl"}},
    {"id": "publish", "type": "git-publish"}
  ], "edges": [
    {"from": "plan", "to": "impl", "artifact": "plan"},
    {"from": "impl", "to": "review", "artifact": "patch"},
    {"from": "impl", "to": "gate", "artifact": "patch"},
    {"from": "review", "to": "gate", "artifact": "review_verdict"},
    {"from": "gate", "to": "publish", "artifact": "patch"}
  ], "schema": 1}`
	d2, err := workflows.ParseDocument([]byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if d1.Hash() != d2.Hash() {
		t.Fatal("hash depends on formatting, not content")
	}
	if len(d1.Hash()) != 64 {
		t.Fatalf("hash %q is not hex sha-256", d1.Hash())
	}

	d2.Nodes[0].Instructions = "Plan the change differently."
	if d1.Hash() == d2.Hash() {
		t.Fatal("hash blind to a content change")
	}
}

func TestParseDocument_NormalizesEmptyEdges(t *testing.T) {
	a, err := workflows.ParseDocument([]byte(`{"schema": 1, "nodes": [{"id": "a", "type": "plan", "harness": "cc", "instructions": "x"}], "edges": []}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := workflows.ParseDocument([]byte(`{"schema": 1, "nodes": [{"id": "a", "type": "plan", "harness": "cc", "instructions": "x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() != b.Hash() {
		t.Fatal(`"edges": [] and absent edges must hash identically`)
	}
}

func TestNodeAndArtifactTypes_Validity(t *testing.T) {
	for _, nt := range []workflows.NodeType{workflows.NodePlan, workflows.NodeImplement, workflows.NodeReview, workflows.NodeGate, workflows.NodeGitPublish} {
		if !nt.Valid() {
			t.Fatalf("%q should be valid", nt)
		}
	}
	if workflows.NodeType("compile").Valid() {
		t.Fatal("unknown node type accepted")
	}
	if !workflows.NodePlan.Agentic() || !workflows.NodeImplement.Agentic() || !workflows.NodeReview.Agentic() {
		t.Fatal("agentic classification wrong")
	}
	if workflows.NodeGate.Agentic() || workflows.NodeGitPublish.Agentic() {
		t.Fatal("gate/git-publish are not agentic")
	}
	if !workflows.ArtifactPatch.Valid() || workflows.ArtifactType("log_ref").Valid() {
		t.Fatal("artifact validity wrong: log_ref is a runtime output, never bindable")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/workflows/`
Expected: FAIL — package does not exist / undefined symbols.

- [ ] **Step 3: Implement the document layer**

`backend/internal/workflows/definition.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/workflows/`
Expected: PASS

- [ ] **Step 5: Run the repo gates**

Run: `task backend:fmt && task backend:vet && task backend:lint && task backend:analyze`
Expected: clean (the new context needs no arch/analyzer changes — `contextOf` treats any new `internal/` segment as a bounded context automatically).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/workflows
git commit -m "feat(backend): workflow definition document with canonical content hash"
```

---

### Task 2: Shape validation — ids, per-type config, edge signatures, multiplicities

Everything checkable without graph traversal: id/config rules, edge endpoint existence, the per-type binding signature table from the plan header. `Validate` is **not** exported yet — the full pipeline assembles in Task 4; until then the shape checker is unexported and tested from an internal test package (stubs never fake success: no caller can mistake shape-only checking for full validation).

**Files:**
- Create: `backend/internal/workflows/validate.go`
- Create: `backend/internal/workflows/validate_test.go` (internal: `package workflows`)

**Interfaces:**
- Consumes: Task 1's types.
- Produces: `workflows.Violation{Path, Message string}` (exported — the service and API map these onto problem-envelope field errors) · unexported `validateShape(d Document) []Violation` · unexported `produces map[NodeType]ArtifactType` (graph tasks reuse it). Violations are appended in deterministic document order.

- [ ] **Step 1: Write the failing tests**

`backend/internal/workflows/validate_test.go`:

```go
package workflows

import (
	"strings"
	"testing"
)

// conveyor is the canonical five-node document as a struct; mutation
// helpers below derive every invalid case from it.
func conveyor() Document {
	return Document{
		Schema: SchemaVersion,
		Nodes: []Node{
			{ID: "plan", Type: NodePlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Type: NodeImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "review", Type: NodeReview, Harness: "cc", Instructions: "Review the patch."},
			{ID: "gate", Type: NodeGate, OnRequestChanges: &RevisionTransition{Target: "impl", MaxRounds: 3}},
			{ID: "publish", Type: NodeGitPublish},
		},
		Edges: []Edge{
			{From: "plan", To: "impl", Artifact: ArtifactPlan},
			{From: "impl", To: "review", Artifact: ArtifactPatch},
			{From: "impl", To: "gate", Artifact: ArtifactPatch},
			{From: "review", To: "gate", Artifact: ArtifactReviewVerdict},
			{From: "gate", To: "publish", Artifact: ArtifactPatch},
		},
	}
}

func wantViolation(t *testing.T, vs []Violation, path, msgPart string) {
	t.Helper()
	for _, v := range vs {
		if v.Path == path && strings.Contains(v.Message, msgPart) {
			return
		}
	}
	t.Fatalf("no violation at %q containing %q; got %+v", path, msgPart, vs)
}

func TestValidateShape_AcceptsTheConveyor(t *testing.T) {
	if vs := validateShape(conveyor()); len(vs) != 0 {
		t.Fatalf("valid document rejected: %+v", vs)
	}
}

func TestValidateShape_NodeRules(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Document)
		path    string
		msgPart string
	}{
		{"no nodes", func(d *Document) { d.Nodes = nil }, "nodes", "at least one node"},
		{"too many nodes", func(d *Document) {
			for i := 0; i < 50; i++ {
				d.Nodes = append(d.Nodes, Node{ID: "x" + strings.Repeat("a", i+1), Type: NodePlan, Harness: "cc", Instructions: "x"})
			}
		}, "nodes", "at most 50 nodes"},
		{"bad id charset", func(d *Document) { d.Nodes[0].ID = "Plan!" }, "nodes[0].id", "must match"},
		{"bad id leading digit", func(d *Document) { d.Nodes[0].ID = "1plan" }, "nodes[0].id", "must match"},
		{"duplicate id", func(d *Document) { d.Nodes[1].ID = "plan" }, "nodes[1].id", "duplicate"},
		{"unknown type", func(d *Document) { d.Nodes[0].Type = "compile" }, "nodes[0].type", "unknown node type"},
		{"agentic without harness", func(d *Document) { d.Nodes[0].Harness = "" }, "nodes[0].harness", "required"},
		{"harness untrimmed", func(d *Document) { d.Nodes[0].Harness = " cc " }, "nodes[0].harness", "whitespace"},
		{"harness too long", func(d *Document) { d.Nodes[0].Harness = strings.Repeat("h", 101) }, "nodes[0].harness", "at most 100"},
		{"agentic without instructions", func(d *Document) { d.Nodes[0].Instructions = "" }, "nodes[0].instructions", "required"},
		{"instructions too long", func(d *Document) { d.Nodes[0].Instructions = strings.Repeat("i", 8193) }, "nodes[0].instructions", "at most 8192"},
		{"gate with harness", func(d *Document) { d.Nodes[3].Harness = "cc" }, "nodes[3].harness", "only agentic"},
		{"gate with instructions", func(d *Document) { d.Nodes[3].Instructions = "decide" }, "nodes[3].instructions", "only agentic"},
		{"publish with harness", func(d *Document) { d.Nodes[4].Harness = "cc" }, "nodes[4].harness", "only agentic"},
		{"revision on non-gate", func(d *Document) {
			d.Nodes[1].OnRequestChanges = &RevisionTransition{Target: "plan", MaxRounds: 1}
		}, "nodes[1].on_request_changes", "only gates"},
		{"revision target unknown", func(d *Document) { d.Nodes[3].OnRequestChanges.Target = "ghost" }, "nodes[3].on_request_changes.target", "unknown node"},
		{"max_rounds zero", func(d *Document) { d.Nodes[3].OnRequestChanges.MaxRounds = 0 }, "nodes[3].on_request_changes.max_rounds", "1-10"},
		{"max_rounds too high", func(d *Document) { d.Nodes[3].OnRequestChanges.MaxRounds = 11 }, "nodes[3].on_request_changes.max_rounds", "1-10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := conveyor()
			c.mutate(&d)
			wantViolation(t, validateShape(d), c.path, c.msgPart)
		})
	}
}

func TestValidateShape_EdgeRules(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Document)
		path    string
		msgPart string
	}{
		{"unknown from", func(d *Document) { d.Edges[0].From = "ghost" }, "edges[0].from", "unknown node"},
		{"unknown to", func(d *Document) { d.Edges[0].To = "ghost" }, "edges[0].to", "unknown node"},
		{"self edge", func(d *Document) { d.Edges[0].To = "plan" }, "edges[0]", "self"},
		{"unknown artifact", func(d *Document) { d.Edges[0].Artifact = "binary" }, "edges[0].artifact", "unknown artifact"},
		{"duplicate edge", func(d *Document) { d.Edges = append(d.Edges, d.Edges[1]) }, "edges[5]", "duplicate"},
		{"too many edges", func(d *Document) {
			for i := 0; i < 200; i++ {
				d.Edges = append(d.Edges, Edge{From: "plan", To: "impl", Artifact: ArtifactPlan})
			}
		}, "edges", "at most 200 edges"},
		{"plan producing patch", func(d *Document) { d.Edges[0].Artifact = ArtifactPatch }, "edges[0].artifact", "produces plan"},
		{"publish producing", func(d *Document) {
			d.Edges = append(d.Edges, Edge{From: "publish", To: "review", Artifact: ArtifactPatch})
		}, "edges[5]", "git-publish produces nothing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := conveyor()
			c.mutate(&d)
			wantViolation(t, validateShape(d), c.path, c.msgPart)
		})
	}
}

func TestValidateShape_ConsumptionMultiplicities(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Document)
		path    string
		msgPart string
	}{
		{"plan consuming anything", func(d *Document) {
			d.Nodes = append(d.Nodes, Node{ID: "plan2", Type: NodePlan, Harness: "cc", Instructions: "x"})
			d.Edges = append(d.Edges, Edge{From: "plan", To: "plan2", Artifact: ArtifactPlan})
		}, "nodes[5]", "consumes nothing"},
		{"implement with two plans", func(d *Document) {
			d.Nodes = append(d.Nodes, Node{ID: "plan2", Type: NodePlan, Harness: "cc", Instructions: "x"})
			d.Edges = append(d.Edges, Edge{From: "plan2", To: "impl", Artifact: ArtifactPlan})
		}, "nodes[1]", "at most 1 plan"},
		{"review without patch", func(d *Document) {
			d.Edges = append(d.Edges[:1], d.Edges[2:]...) // drop impl→review
		}, "nodes[2]", "exactly 1 patch"},
		{"review consuming verdict", func(d *Document) {
			d.Nodes = append(d.Nodes, Node{ID: "review2", Type: NodeReview, Harness: "cc", Instructions: "x"})
			d.Edges = append(d.Edges,
				Edge{From: "impl", To: "review2", Artifact: ArtifactPatch},
				Edge{From: "review", To: "review2", Artifact: ArtifactReviewVerdict})
		}, "nodes[5]", "cannot consume review_verdict"},
		{"gate without examined", func(d *Document) {
			d.Edges = append(d.Edges[:2], d.Edges[3:]...) // drop impl→gate patch
		}, "nodes[3]", "exactly one plan or patch"},
		{"gate with two examined", func(d *Document) {
			d.Edges = append(d.Edges, Edge{From: "plan", To: "gate", Artifact: ArtifactPlan})
		}, "nodes[3]", "exactly one plan or patch"},
		{"publish without patch", func(d *Document) {
			d.Edges = d.Edges[:4] // drop gate→publish
		}, "nodes[4]", "exactly 1 patch"},
		{"implement consuming verdict", func(d *Document) {
			d.Nodes = append(d.Nodes, Node{ID: "impl2", Type: NodeImplement, Harness: "cc", Instructions: "x"})
			d.Edges = append(d.Edges, Edge{From: "review", To: "impl2", Artifact: ArtifactReviewVerdict})
		}, "nodes[5]", "cannot consume review_verdict"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := conveyor()
			c.mutate(&d)
			wantViolation(t, validateShape(d), c.path, c.msgPart)
		})
	}
}
```

Note the deliberate asymmetry: node-level consumption violations anchor at the **consumer's** path (`nodes[i]`), edge-level ones at the edge (`edges[i]…`) — the editor highlights the right element either way.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/workflows/`
Expected: FAIL — `validateShape` undefined.

- [ ] **Step 3: Implement shape validation**

`backend/internal/workflows/validate.go`:

```go
package workflows

import (
	"fmt"
	"regexp"
	"strings"
)

// Violation is one validation failure. Path addresses the offending
// element in the document ("nodes[2].harness", "edges[0]", "nodes");
// the API maps violations onto problem-envelope field errors verbatim.
type Violation struct {
	Path    string
	Message string
}

// Validation pins (plan header). Byte counts, house convention.
const (
	maxNodes           = 50
	maxEdges           = 200
	maxInstructionsLen = 8192
	maxHarnessLen      = 100
	maxRevisionRounds  = 10
)

var nodeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// produces is the fixed output signature of the producing types. Gates
// pass through their examined type (checked in the structural phase);
// git-publish produces nothing.
var produces = map[NodeType]ArtifactType{
	NodePlan:      ArtifactPlan,
	NodeImplement: ArtifactPatch,
	NodeReview:    ArtifactReviewVerdict,
}

// validateShape checks everything checkable without graph traversal:
// counts, ids, per-type config, edge endpoints, and the binding
// signature table from the plan header. Violations come back in
// deterministic document order.
func validateShape(d Document) []Violation {
	var vs []Violation
	if len(d.Nodes) == 0 {
		return []Violation{{Path: "nodes", Message: "a definition needs at least one node"}}
	}
	if len(d.Nodes) > maxNodes {
		vs = append(vs, Violation{Path: "nodes", Message: fmt.Sprintf("at most %d nodes", maxNodes)})
	}
	if len(d.Edges) > maxEdges {
		vs = append(vs, Violation{Path: "edges", Message: fmt.Sprintf("at most %d edges", maxEdges)})
	}

	index := make(map[string]int, len(d.Nodes))
	for i, n := range d.Nodes {
		path := fmt.Sprintf("nodes[%d]", i)
		if !nodeIDPattern.MatchString(n.ID) {
			vs = append(vs, Violation{Path: path + ".id", Message: "must match ^[a-z][a-z0-9_-]{0,63}$"})
		} else if _, dup := index[n.ID]; dup {
			vs = append(vs, Violation{Path: path + ".id", Message: fmt.Sprintf("duplicate node id %q", n.ID)})
		} else {
			index[n.ID] = i
		}
		vs = append(vs, validateNodeConfig(path, n)...)
	}

	seen := make(map[Edge]bool, len(d.Edges))
	for i, e := range d.Edges {
		path := fmt.Sprintf("edges[%d]", i)
		ok := true
		if _, exists := index[e.From]; !exists {
			vs = append(vs, Violation{Path: path + ".from", Message: fmt.Sprintf("unknown node %q", e.From)})
			ok = false
		}
		if _, exists := index[e.To]; !exists {
			vs = append(vs, Violation{Path: path + ".to", Message: fmt.Sprintf("unknown node %q", e.To)})
			ok = false
		}
		if !e.Artifact.Valid() {
			vs = append(vs, Violation{Path: path + ".artifact", Message: fmt.Sprintf("unknown artifact type %q", e.Artifact)})
			ok = false
		}
		if !ok {
			continue
		}
		if e.From == e.To {
			vs = append(vs, Violation{Path: path, Message: "self edges are not allowed"})
			continue
		}
		if seen[e] {
			vs = append(vs, Violation{Path: path, Message: fmt.Sprintf("duplicate edge %s→%s (%s)", e.From, e.To, e.Artifact)})
			continue
		}
		seen[e] = true

		switch from := d.Nodes[index[e.From]]; {
		case from.Type == NodeGitPublish:
			vs = append(vs, Violation{Path: path, Message: "git-publish produces nothing; it cannot have outgoing edges"})
		case from.Type.Agentic() && e.Artifact != produces[from.Type]:
			vs = append(vs, Violation{Path: path + ".artifact", Message: fmt.Sprintf("%s produces %s, not %s", from.Type, produces[from.Type], e.Artifact)})
		}
		// Gate sources pass through their examined type — checked in the
		// structural phase once the examined edge is known.
	}

	// Consumption multiplicities per node, counted over well-formed edges.
	in := map[string]map[ArtifactType]int{}
	for _, e := range d.Edges {
		if _, ok := index[e.From]; !ok || e.From == e.To || !e.Artifact.Valid() {
			continue
		}
		if _, ok := index[e.To]; !ok {
			continue
		}
		if in[e.To] == nil {
			in[e.To] = map[ArtifactType]int{}
		}
		in[e.To][e.Artifact]++
	}
	for i, n := range d.Nodes {
		if index[n.ID] != i { // duplicate id row: counted once, on the first
			continue
		}
		vs = append(vs, validateConsumption(fmt.Sprintf("nodes[%d]", i), n, in[n.ID])...)
	}
	return vs
}

func validateNodeConfig(path string, n Node) []Violation {
	var vs []Violation
	if !n.Type.Valid() {
		return []Violation{{Path: path + ".type", Message: fmt.Sprintf("unknown node type %q", n.Type)}}
	}
	if n.Type.Agentic() {
		switch {
		case n.Harness == "":
			vs = append(vs, Violation{Path: path + ".harness", Message: "required on agentic nodes"})
		case strings.TrimSpace(n.Harness) != n.Harness:
			vs = append(vs, Violation{Path: path + ".harness", Message: "must not have surrounding whitespace"})
		case len(n.Harness) > maxHarnessLen:
			vs = append(vs, Violation{Path: path + ".harness", Message: fmt.Sprintf("at most %d bytes", maxHarnessLen)})
		}
		switch {
		case n.Instructions == "":
			vs = append(vs, Violation{Path: path + ".instructions", Message: "required on agentic nodes"})
		case len(n.Instructions) > maxInstructionsLen:
			vs = append(vs, Violation{Path: path + ".instructions", Message: fmt.Sprintf("at most %d bytes", maxInstructionsLen)})
		}
	} else {
		if n.Harness != "" {
			vs = append(vs, Violation{Path: path + ".harness", Message: "only agentic nodes take a harness"})
		}
		if n.Instructions != "" {
			vs = append(vs, Violation{Path: path + ".instructions", Message: "only agentic nodes take instructions"})
		}
	}
	if n.OnRequestChanges != nil {
		if n.Type != NodeGate {
			vs = append(vs, Violation{Path: path + ".on_request_changes", Message: "only gates declare revision transitions"})
		} else {
			if r := n.OnRequestChanges.MaxRounds; r < 1 || r > maxRevisionRounds {
				vs = append(vs, Violation{Path: path + ".on_request_changes.max_rounds", Message: fmt.Sprintf("must be 1-%d", maxRevisionRounds)})
			}
		}
	}
	return vs
}

// validateConsumption applies the per-type input signature table from
// the plan header to one node's incoming-edge counts.
func validateConsumption(path string, n Node, in map[ArtifactType]int) []Violation {
	var vs []Violation
	plans, patches, verdicts := in[ArtifactPlan], in[ArtifactPatch], in[ArtifactReviewVerdict]
	switch n.Type {
	case NodePlan:
		if plans+patches+verdicts > 0 {
			vs = append(vs, Violation{Path: path, Message: "a plan node consumes nothing"})
		}
	case NodeImplement:
		if plans > 1 {
			vs = append(vs, Violation{Path: path, Message: "implement consumes at most 1 plan"})
		}
		if patches > 1 {
			vs = append(vs, Violation{Path: path, Message: "implement consumes at most 1 patch"})
		}
		if verdicts > 0 {
			vs = append(vs, Violation{Path: path, Message: "implement cannot consume review_verdict"})
		}
	case NodeReview:
		if patches != 1 {
			vs = append(vs, Violation{Path: path, Message: "review consumes exactly 1 patch"})
		}
		if plans > 0 || verdicts > 0 {
			vs = append(vs, Violation{Path: path, Message: "review cannot consume review_verdict or plan"})
		}
	case NodeGate:
		if plans+patches != 1 {
			vs = append(vs, Violation{Path: path, Message: "a gate examines exactly one plan or patch"})
		}
		if verdicts > 1 {
			vs = append(vs, Violation{Path: path, Message: "a gate consumes at most 1 review_verdict"})
		}
	case NodeGitPublish:
		if patches != 1 {
			vs = append(vs, Violation{Path: path, Message: "git-publish consumes exactly 1 patch"})
		}
		if plans > 0 || verdicts > 0 {
			vs = append(vs, Violation{Path: path, Message: "git-publish consumes only a patch"})
		}
	}
	return vs
}
```

The revision-target existence check needs the fully-built `index` map (a target may legally reference a *later* node), so it cannot live in `validateNodeConfig`. Insert this block into `validateShape` **after** the edge loop and before the consumption loop:

```go
	for i, n := range d.Nodes {
		if n.Type == NodeGate && n.OnRequestChanges != nil {
			if _, ok := index[n.OnRequestChanges.Target]; !ok {
				vs = append(vs, Violation{
					Path:    fmt.Sprintf("nodes[%d].on_request_changes.target", i),
					Message: fmt.Sprintf("unknown node %q", n.OnRequestChanges.Target),
				})
			}
		}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/workflows/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/workflows
git commit -m "feat(backend): definition shape validation"
```

---

### Task 3: Graph machinery — cycles, reachability, dominators, artifact origins

The structural invariants (Task 4) are all statements about paths. This task lands the graph view of a shape-valid document: Kahn cycle detection with deterministic output, strict ancestor/descendant sets, dominator sets over a virtual root, and artifact-origin resolution through pass-through gates. All unexported; property tests (Task 5) exercise these directly from the internal package.

**Files:**
- Create: `backend/internal/workflows/graph.go`
- Create: `backend/internal/workflows/graph_test.go` (internal: `package workflows`)

**Interfaces:**
- Consumes: Task 1's types; built only after Task 2's `validateShape` passes.
- Produces (unexported, used by Task 4 and property tests):
  - `newGraph(d Document) *graph`
  - `(*graph).node(id string) Node`
  - `(*graph).cycle() []string` — nil when acyclic (and records the topological order internally); otherwise the sorted ids of unresolvable nodes
  - `(*graph).ancestors(id string) map[string]bool` / `(*graph).descendants(id string) map[string]bool` — strict (exclude id)
  - `(*graph).dominators() map[string]map[string]bool` — call only after `cycle()` returned nil
  - `(*graph).examined(gateID string) Edge` / `(*graph).origin(e Edge) string`

- [ ] **Step 1: Write the failing tests**

`backend/internal/workflows/graph_test.go`:

```go
package workflows

import (
	"slices"
	"testing"
)

// mk builds a bare document for graph tests: shape-level config is
// irrelevant here, only ids, types, and edges matter.
func mk(nodes []Node, edges []Edge) Document {
	return Document{Schema: SchemaVersion, Nodes: nodes, Edges: edges}
}

func ids(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestGraphCycle_AcceptsTheConveyorAndOrdersIt(t *testing.T) {
	g := newGraph(conveyor())
	if got := g.cycle(); got != nil {
		t.Fatalf("conveyor reported cyclic: %v", got)
	}
	pos := map[string]int{}
	for i, id := range g.order {
		pos[id] = i
	}
	for _, e := range g.d.Edges {
		if pos[e.From] >= pos[e.To] {
			t.Fatalf("order %v is not topological: %s→%s", g.order, e.From, e.To)
		}
	}
}

func TestGraphCycle_ReportsBackEdges(t *testing.T) {
	d := conveyor()
	d.Edges = append(d.Edges, Edge{From: "publish", To: "plan", Artifact: ArtifactPatch})
	got := newGraph(d).cycle()
	if got == nil {
		t.Fatal("cycle not detected")
	}
	if !slices.Contains(got, "plan") || !slices.IsSorted(got) {
		t.Fatalf("cycle report %v: want sorted, containing the cycle members", got)
	}
}

func TestGraphReachability_StrictAncestorsAndDescendants(t *testing.T) {
	g := newGraph(conveyor())
	if got := ids(g.ancestors("publish")); !slices.Equal(got, []string{"gate", "impl", "plan", "review"}) {
		t.Fatalf("ancestors(publish) = %v", got)
	}
	if got := ids(g.descendants("plan")); !slices.Equal(got, []string{"gate", "impl", "publish", "review"}) {
		t.Fatalf("descendants(plan) = %v", got)
	}
	if g.ancestors("plan")["plan"] || g.descendants("publish")["publish"] {
		t.Fatal("reachability must be strict (exclude the node itself)")
	}
}

func TestGraphDominators_DiamondAndMultiRoot(t *testing.T) {
	// a → b → d, a → c → d: a dominates everything; b and c dominate
	// nothing but themselves.
	diamond := mk(
		[]Node{{ID: "a", Type: NodePlan}, {ID: "b", Type: NodeImplement}, {ID: "c", Type: NodeImplement}, {ID: "d", Type: NodeGate}},
		[]Edge{
			{From: "a", To: "b", Artifact: ArtifactPlan},
			{From: "a", To: "c", Artifact: ArtifactPlan},
			{From: "b", To: "d", Artifact: ArtifactPatch},
			{From: "c", To: "d", Artifact: ArtifactPatch},
		})
	g := newGraph(diamond)
	if g.cycle() != nil {
		t.Fatal("diamond is acyclic")
	}
	dom := g.dominators()
	if got := ids(dom["d"]); !slices.Equal(got, []string{"a", "d"}) {
		t.Fatalf("dom(d) = %v, want [a d]", got)
	}
	if got := ids(dom["b"]); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("dom(b) = %v, want [a b]", got)
	}

	// Two roots feeding one node: nothing dominates x but itself (the
	// virtual root stays implicit).
	multi := mk(
		[]Node{{ID: "r1", Type: NodePlan}, {ID: "r2", Type: NodePlan}, {ID: "x", Type: NodeGate}},
		[]Edge{
			{From: "r1", To: "x", Artifact: ArtifactPlan},
			{From: "r2", To: "x", Artifact: ArtifactPlan},
		})
	g = newGraph(multi)
	if g.cycle() != nil {
		t.Fatal("multi-root is acyclic")
	}
	if got := ids(g.dominators()["x"]); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("dom(x) = %v, want [x]", got)
	}
}

func TestGraphExaminedAndOrigin_WalkThroughGateChains(t *testing.T) {
	// impl → gate1 → gate2 → publish: both gates pass the same patch
	// through; its origin is impl everywhere.
	d := mk(
		[]Node{
			{ID: "impl", Type: NodeImplement, Harness: "cc", Instructions: "x"},
			{ID: "gate1", Type: NodeGate},
			{ID: "gate2", Type: NodeGate},
			{ID: "publish", Type: NodeGitPublish},
		},
		[]Edge{
			{From: "impl", To: "gate1", Artifact: ArtifactPatch},
			{From: "gate1", To: "gate2", Artifact: ArtifactPatch},
			{From: "gate2", To: "publish", Artifact: ArtifactPatch},
		})
	g := newGraph(d)
	if ex := g.examined("gate1"); ex.From != "impl" || ex.Artifact != ArtifactPatch {
		t.Fatalf("examined(gate1) = %+v", ex)
	}
	if ex := g.examined("gate2"); ex.From != "gate1" {
		t.Fatalf("examined(gate2) = %+v", ex)
	}
	last := d.Edges[2]
	if got := g.origin(last); got != "impl" {
		t.Fatalf("origin(gate2→publish) = %q, want impl", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/workflows/`
Expected: FAIL — `newGraph` undefined.

- [ ] **Step 3: Implement the graph**

`backend/internal/workflows/graph.go`:

```go
package workflows

import "sort"

// graph is the adjacency view of a shape-valid document. The structural
// phase builds it only after validateShape passes, so node/edge lookups
// never miss.
type graph struct {
	d     Document
	index map[string]int
	out   map[string][]Edge
	in    map[string][]Edge
	order []string // topological order; recorded by cycle() when acyclic
}

func newGraph(d Document) *graph {
	g := &graph{
		d:     d,
		index: make(map[string]int, len(d.Nodes)),
		out:   make(map[string][]Edge, len(d.Nodes)),
		in:    make(map[string][]Edge, len(d.Nodes)),
	}
	for i, n := range d.Nodes {
		g.index[n.ID] = i
	}
	for _, e := range d.Edges {
		g.out[e.From] = append(g.out[e.From], e)
		g.in[e.To] = append(g.in[e.To], e)
	}
	return g
}

func (g *graph) node(id string) Node { return g.d.Nodes[g.index[id]] }

// cycle runs Kahn's algorithm with lexicographic tie-breaking. On an
// acyclic graph it records the topological order and returns nil;
// otherwise it returns the sorted ids of the nodes it could not resolve
// (the cycle members and everything reachable only through them).
func (g *graph) cycle() []string {
	deg := make(map[string]int, len(g.d.Nodes))
	var ready []string
	for _, n := range g.d.Nodes {
		deg[n.ID] = len(g.in[n.ID])
		if deg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	order := make([]string, 0, len(g.d.Nodes))
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, e := range g.out[id] {
			deg[e.To]--
			if deg[e.To] == 0 {
				ready = append(ready, e.To)
			}
		}
	}
	if len(order) == len(g.d.Nodes) {
		g.order = order
		return nil
	}
	var stuck []string
	for id, n := range deg {
		if n > 0 {
			stuck = append(stuck, id)
		}
	}
	sort.Strings(stuck)
	return stuck
}

// ancestors returns the strict transitive-predecessor set of id.
func (g *graph) ancestors(id string) map[string]bool {
	return g.walk(id, g.in, func(e Edge) string { return e.From })
}

// descendants returns the strict transitive-successor set of id.
func (g *graph) descendants(id string) map[string]bool {
	return g.walk(id, g.out, func(e Edge) string { return e.To })
}

func (g *graph) walk(id string, adj map[string][]Edge, next func(Edge) string) map[string]bool {
	seen := map[string]bool{}
	stack := []string{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range adj[cur] {
			if n := next(e); !seen[n] {
				seen[n] = true
				stack = append(stack, n)
			}
		}
	}
	delete(seen, id)
	return seen
}

// dominators computes dom(n): every node lying on every path from the
// virtual root (drawn over all in-degree-0 nodes) to n, n included; the
// virtual root itself stays implicit. Iterative set intersection in
// topological order — document graphs are ≤50 nodes, so clarity beats
// Lengauer-Tarjan. For any node the dominator set is totally ordered by
// dominance, which is what makes "nearest dominating gate" (Task 4)
// well defined. Call only after cycle() returned nil.
func (g *graph) dominators() map[string]map[string]bool {
	dom := make(map[string]map[string]bool, len(g.order))
	for _, id := range g.order {
		var inter map[string]bool
		for i, e := range g.in[id] {
			p := dom[e.From]
			if i == 0 {
				inter = make(map[string]bool, len(p)+1)
				for k := range p {
					inter[k] = true
				}
				continue
			}
			for k := range inter {
				if !p[k] {
					delete(inter, k)
				}
			}
		}
		if inter == nil {
			inter = make(map[string]bool, 1)
		}
		inter[id] = true
		dom[id] = inter
	}
	return dom
}

// examined returns a gate's examined input edge — its single plan|patch
// binding. Shape validation guarantees exactly one exists.
func (g *graph) examined(gateID string) Edge {
	for _, e := range g.in[gateID] {
		if e.Artifact == ArtifactPlan || e.Artifact == ArtifactPatch {
			return e
		}
	}
	panic("workflows: examined called on a gate without an examined input")
}

// origin walks an artifact edge back through pass-through gates to the
// node that actually produced it (plan header, decision 2). Acyclicity
// bounds the walk.
func (g *graph) origin(e Edge) string {
	for g.node(e.From).Type == NodeGate {
		e = g.examined(e.From)
	}
	return e.From
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/workflows/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/workflows
git commit -m "feat(backend): definition graph machinery for cycles, dominators, and artifact origins"
```

---

### Task 4: Structural invariants — the validator assembles

The §2 invariants land and `Validate` goes public: gate pass-through typing, linear patch lineage, revision-transition dominance + disjoint loops, gate-dominates-exact-patch, one-gate-per-publish. After this task the validator is feature-complete.

**Files:**
- Modify: `backend/internal/workflows/validate.go`
- Modify: `backend/internal/workflows/validate_test.go`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: `workflows.Validate(d Document) []Violation` — empty result means valid; shape violations short-circuit the graph phases; a cycle short-circuits the path-based phases. This is the signature the service (Task 8), fuzz targets (Task 6), and Plan 05's run-creation path call.

- [ ] **Step 1: Write the failing tests**

Append to `backend/internal/workflows/validate_test.go`:

```go
func TestValidate_AcceptsTheConveyor(t *testing.T) {
	if vs := Validate(conveyor()); len(vs) != 0 {
		t.Fatalf("conveyor rejected: %+v", vs)
	}
}

func TestValidate_AcceptsChainedImplements(t *testing.T) {
	// plan → impl1 → impl2 → review → gate → publish: two patch
	// producers in ONE lineage — a chain, not a merge.
	d := Document{
		Schema: SchemaVersion,
		Nodes: []Node{
			{ID: "plan", Type: NodePlan, Harness: "cc", Instructions: "x"},
			{ID: "impl1", Type: NodeImplement, Harness: "cc", Instructions: "x"},
			{ID: "impl2", Type: NodeImplement, Harness: "cc", Instructions: "x"},
			{ID: "review", Type: NodeReview, Harness: "cc", Instructions: "x"},
			{ID: "gate", Type: NodeGate},
			{ID: "publish", Type: NodeGitPublish},
		},
		Edges: []Edge{
			{From: "plan", To: "impl1", Artifact: ArtifactPlan},
			{From: "impl1", To: "impl2", Artifact: ArtifactPatch},
			{From: "impl2", To: "review", Artifact: ArtifactPatch},
			{From: "impl2", To: "gate", Artifact: ArtifactPatch},
			{From: "review", To: "gate", Artifact: ArtifactReviewVerdict},
			{From: "gate", To: "publish", Artifact: ArtifactPatch},
		},
	}
	if vs := Validate(d); len(vs) != 0 {
		t.Fatalf("chained implements rejected: %+v", vs)
	}
}

func TestValidate_ReportsCyclesAndStopsThere(t *testing.T) {
	// gate → impl is shape-legal (implement consumes ≤1 patch, the gate
	// passes through its examined type), so the cycle phase is genuinely
	// what rejects it — and it short-circuits the later phases.
	d := conveyor()
	d.Edges = append(d.Edges, Edge{From: "gate", To: "impl", Artifact: ArtifactPatch})
	vs := Validate(d)
	if len(vs) != 1 || vs[0].Path != "edges" || !strings.Contains(vs[0].Message, "acyclic") {
		t.Fatalf("want exactly the cycle violation, got %+v", vs)
	}
}

func TestValidate_RejectsDivergentPatchLineages(t *testing.T) {
	// plan fans out to implA and implB; the gate examines implA's patch
	// but consumes a verdict about implB's — the artifact-divergence bug
	// class §2 exists to kill.
	d := Document{
		Schema: SchemaVersion,
		Nodes: []Node{
			{ID: "plan", Type: NodePlan, Harness: "cc", Instructions: "x"},
			{ID: "impl_a", Type: NodeImplement, Harness: "cc", Instructions: "x"},
			{ID: "impl_b", Type: NodeImplement, Harness: "cc", Instructions: "x"},
			{ID: "review_b", Type: NodeReview, Harness: "cc", Instructions: "x"},
			{ID: "gate", Type: NodeGate},
		},
		Edges: []Edge{
			{From: "plan", To: "impl_a", Artifact: ArtifactPlan},
			{From: "plan", To: "impl_b", Artifact: ArtifactPlan},
			{From: "impl_a", To: "gate", Artifact: ArtifactPatch},
			{From: "impl_b", To: "review_b", Artifact: ArtifactPatch},
			{From: "review_b", To: "gate", Artifact: ArtifactReviewVerdict},
		},
	}
	wantViolation(t, Validate(d), "nodes[4]", "divergent patch lineages")
}

func TestValidate_GatePassThroughTypeIsPinned(t *testing.T) {
	// The gate examines a patch but one outgoing edge claims to carry a
	// plan.
	d := conveyor()
	d.Nodes = append(d.Nodes, Node{ID: "impl2", Type: NodeImplement, Harness: "cc", Instructions: "x"})
	d.Edges = append(d.Edges, Edge{From: "gate", To: "impl2", Artifact: ArtifactPlan})
	wantViolation(t, Validate(d), "edges[5].artifact", "passes through patch")
}

func TestValidate_RevisionTransitionRules(t *testing.T) {
	t.Run("target must be agentic", func(t *testing.T) {
		d := conveyor()
		d.Nodes[3].OnRequestChanges.Target = "publish"
		wantViolation(t, Validate(d), "nodes[3].on_request_changes.target", "agentic")
	})
	t.Run("target must dominate the gate", func(t *testing.T) {
		d := conveyor()
		// review does NOT dominate the gate: the impl→gate patch edge
		// bypasses it.
		d.Nodes[3].OnRequestChanges.Target = "review"
		wantViolation(t, Validate(d), "nodes[3].on_request_changes.target", "dominate")
	})
	t.Run("overlapping loops rejected", func(t *testing.T) {
		// impl → gate1 → gate2, both looping back to impl: gate1's slice
		// {impl, gate1} sits inside gate2's {impl, gate1, gate2}.
		d := Document{
			Schema: SchemaVersion,
			Nodes: []Node{
				{ID: "impl", Type: NodeImplement, Harness: "cc", Instructions: "x"},
				{ID: "gate1", Type: NodeGate, OnRequestChanges: &RevisionTransition{Target: "impl", MaxRounds: 2}},
				{ID: "gate2", Type: NodeGate, OnRequestChanges: &RevisionTransition{Target: "impl", MaxRounds: 2}},
			},
			Edges: []Edge{
				{From: "impl", To: "gate1", Artifact: ArtifactPatch},
				{From: "gate1", To: "gate2", Artifact: ArtifactPatch},
			},
		}
		wantViolation(t, Validate(d), "nodes[2].on_request_changes", "overlap")
	})
}

func TestValidate_PublishRules(t *testing.T) {
	t.Run("publish without a dominating gate", func(t *testing.T) {
		d := Document{
			Schema: SchemaVersion,
			Nodes: []Node{
				{ID: "impl", Type: NodeImplement, Harness: "cc", Instructions: "x"},
				{ID: "publish", Type: NodeGitPublish},
			},
			Edges: []Edge{{From: "impl", To: "publish", Artifact: ArtifactPatch}},
		}
		wantViolation(t, Validate(d), "nodes[1]", "dominated by a gate")
	})
	t.Run("implement between gate and publish diverges the artifact", func(t *testing.T) {
		// gate approves impl1's patch, but impl2 rewrites it before
		// publish: the published artifact is not the examined one.
		d := Document{
			Schema: SchemaVersion,
			Nodes: []Node{
				{ID: "impl1", Type: NodeImplement, Harness: "cc", Instructions: "x"},
				{ID: "gate", Type: NodeGate},
				{ID: "impl2", Type: NodeImplement, Harness: "cc", Instructions: "x"},
				{ID: "publish", Type: NodeGitPublish},
			},
			Edges: []Edge{
				{From: "impl1", To: "gate", Artifact: ArtifactPatch},
				{From: "gate", To: "impl2", Artifact: ArtifactPatch},
				{From: "impl2", To: "publish", Artifact: ArtifactPatch},
			},
		}
		wantViolation(t, Validate(d), "nodes[3]", "dominated by a gate")
	})
	t.Run("one gate cannot authorize two publishes", func(t *testing.T) {
		d := Document{
			Schema: SchemaVersion,
			Nodes: []Node{
				{ID: "impl", Type: NodeImplement, Harness: "cc", Instructions: "x"},
				{ID: "gate", Type: NodeGate},
				{ID: "pub1", Type: NodeGitPublish},
				{ID: "pub2", Type: NodeGitPublish},
			},
			Edges: []Edge{
				{From: "impl", To: "gate", Artifact: ArtifactPatch},
				{From: "gate", To: "pub1", Artifact: ArtifactPatch},
				{From: "gate", To: "pub2", Artifact: ArtifactPatch},
			},
		}
		wantViolation(t, Validate(d), "nodes[3]", "already authorizes")
	})
	t.Run("chained gates each authorize their own publish", func(t *testing.T) {
		// impl → gate1 → gate2; gate1 authorizes pub1, gate2 (nearest to
		// pub2) authorizes pub2. Same patch, two approvals — legal.
		d := Document{
			Schema: SchemaVersion,
			Nodes: []Node{
				{ID: "impl", Type: NodeImplement, Harness: "cc", Instructions: "x"},
				{ID: "gate1", Type: NodeGate},
				{ID: "gate2", Type: NodeGate},
				{ID: "pub1", Type: NodeGitPublish},
				{ID: "pub2", Type: NodeGitPublish},
			},
			Edges: []Edge{
				{From: "impl", To: "gate1", Artifact: ArtifactPatch},
				{From: "gate1", To: "gate2", Artifact: ArtifactPatch},
				{From: "gate1", To: "pub1", Artifact: ArtifactPatch},
				{From: "gate2", To: "pub2", Artifact: ArtifactPatch},
			},
		}
		if vs := Validate(d); len(vs) != 0 {
			t.Fatalf("chained gates rejected: %+v", vs)
		}
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/workflows/`
Expected: FAIL — `Validate` undefined.

- [ ] **Step 3: Implement the structural phase**

Append to `backend/internal/workflows/validate.go` (add `"sort"` to imports):

```go
// Validate checks d against every §2 invariant (plan header, pinned
// formalizations). Shape violations short-circuit — the graph phases
// assume well-formed nodes and edges; a cycle short-circuits the
// path-based phases. An empty result means the document is valid.
func Validate(d Document) []Violation {
	if vs := validateShape(d); len(vs) > 0 {
		return vs
	}
	g := newGraph(d)
	if stuck := g.cycle(); stuck != nil {
		return []Violation{{Path: "edges", Message: "the dataflow graph must be acyclic; unresolvable nodes: " + strings.Join(stuck, ", ")}}
	}
	var vs []Violation
	vs = append(vs, g.validateGatePassThrough()...)
	vs = append(vs, g.validateLineage()...)
	dom := g.dominators()
	vs = append(vs, g.validateRevisions(dom)...)
	vs = append(vs, g.validatePublishes(dom)...)
	return vs
}

// validateGatePassThrough pins decision 2: a gate's outgoing edges all
// carry exactly its examined artifact type.
func (g *graph) validateGatePassThrough() []Violation {
	var vs []Violation
	for _, n := range g.d.Nodes {
		if n.Type != NodeGate {
			continue
		}
		ex := g.examined(n.ID)
		for j, e := range g.d.Edges {
			if e.From == n.ID && e.Artifact != ex.Artifact {
				vs = append(vs, Violation{
					Path:    fmt.Sprintf("edges[%d].artifact", j),
					Message: fmt.Sprintf("gate %q passes through %s, not %s", n.ID, ex.Artifact, e.Artifact),
				})
			}
		}
	}
	return vs
}

// validateLineage pins decision 3: per node, the implement nodes in its
// strict ancestry must be totally ordered by ancestry — one lineage,
// never a merge of two.
func (g *graph) validateLineage() []Violation {
	anc := make(map[string]map[string]bool, len(g.d.Nodes))
	for _, n := range g.d.Nodes {
		anc[n.ID] = g.ancestors(n.ID)
	}
	var vs []Violation
	for i, n := range g.d.Nodes {
		var producers []string
		for _, p := range g.d.Nodes { // document order → deterministic pairs
			if p.Type == NodeImplement && anc[n.ID][p.ID] {
				producers = append(producers, p.ID)
			}
		}
		for a := 0; a < len(producers); a++ {
			for b := a + 1; b < len(producers); b++ {
				pa, pb := producers[a], producers[b]
				if !anc[pa][pb] && !anc[pb][pa] {
					vs = append(vs, Violation{
						Path:    fmt.Sprintf("nodes[%d]", i),
						Message: fmt.Sprintf("%q merges divergent patch lineages: implement ancestors %q and %q are unordered (V1: patch lineage is linear)", n.ID, pa, pb),
					})
				}
			}
		}
	}
	return vs
}

// validateRevisions pins invariant 5: agentic target, strict dominance,
// pairwise-disjoint loop slices.
func (g *graph) validateRevisions(dom map[string]map[string]bool) []Violation {
	var vs []Violation
	type loop struct {
		gateID string
		slice  map[string]bool
	}
	var loops []loop
	for i, n := range g.d.Nodes {
		if n.Type != NodeGate || n.OnRequestChanges == nil {
			continue
		}
		path := fmt.Sprintf("nodes[%d].on_request_changes", i)
		target := n.OnRequestChanges.Target
		if !g.node(target).Type.Agentic() {
			vs = append(vs, Violation{Path: path + ".target", Message: "revision target must be an agentic node"})
			continue
		}
		if !dom[n.ID][target] {
			vs = append(vs, Violation{Path: path + ".target", Message: fmt.Sprintf("revision target %q must dominate its gate %q", target, n.ID)})
			continue
		}
		slice := map[string]bool{target: true, n.ID: true}
		desc := g.descendants(target)
		for a := range g.ancestors(n.ID) {
			if desc[a] {
				slice[a] = true
			}
		}
		for _, prior := range loops {
			var shared []string
			for id := range slice {
				if prior.slice[id] {
					shared = append(shared, id)
				}
			}
			if len(shared) > 0 {
				sort.Strings(shared)
				vs = append(vs, Violation{
					Path:    path,
					Message: fmt.Sprintf("revision loops of gates %q and %q overlap on %s (V1: loops are disjoint)", prior.gateID, n.ID, strings.Join(shared, ", ")),
				})
			}
		}
		loops = append(loops, loop{gateID: n.ID, slice: slice})
	}
	return vs
}

// validatePublishes pins invariants 6 and 7: every git-publish has an
// eligible dominating gate examining exactly the patch it consumes, and
// no gate authorizes two publishes (nearest-eligible assignment).
func (g *graph) validatePublishes(dom map[string]map[string]bool) []Violation {
	var vs []Violation
	claimed := map[string]string{} // gate id → publish id
	for i, n := range g.d.Nodes {
		if n.Type != NodeGitPublish {
			continue
		}
		path := fmt.Sprintf("nodes[%d]", i)
		var patchEdge Edge
		for _, e := range g.in[n.ID] {
			if e.Artifact == ArtifactPatch {
				patchEdge = e
				break
			}
		}
		origin := g.origin(patchEdge)
		anc := g.ancestors(n.ID)

		var nearest string
		nearestDepth := -1
		for _, cand := range g.d.Nodes {
			if cand.Type != NodeGate || !dom[n.ID][cand.ID] {
				continue
			}
			ex := g.examined(cand.ID)
			if ex.Artifact != ArtifactPatch || g.origin(ex) != origin {
				continue
			}
			between := g.descendants(cand.ID)
			divergent := false
			for id := range between {
				if anc[id] && g.node(id).Type == NodeImplement {
					divergent = true
					break
				}
			}
			if divergent {
				continue
			}
			// dom(P) is a chain, so depth (= |dom(G)|) orders candidates;
			// the deepest eligible gate is the nearest one.
			if depth := len(dom[cand.ID]); depth > nearestDepth {
				nearest, nearestDepth = cand.ID, depth
			}
		}
		if nearest == "" {
			vs = append(vs, Violation{
				Path:    path,
				Message: fmt.Sprintf("%q must be dominated by a gate examining exactly the patch it publishes (spec §2: never publish-then-gate)", n.ID),
			})
			continue
		}
		if other, taken := claimed[nearest]; taken {
			vs = append(vs, Violation{
				Path:    path,
				Message: fmt.Sprintf("gate %q already authorizes %q; an ApprovedPatch authorization is single-use, so each publish needs its own gate", nearest, other),
			})
			continue
		}
		claimed[nearest] = n.ID
	}
	return vs
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/workflows/`
Expected: PASS (all Task 1–4 tests).

- [ ] **Step 5: Run the repo gates**

Run: `task backend:fmt && task backend:vet && task backend:lint && task backend:analyze && task backend:test`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/workflows
git commit -m "feat(backend): structural invariants complete the definition validator"
```

---

### Task 5: Property tests (`rapid`) and the `property` tag plumbing

Property coverage begins (spec §7: DAG resolution/cycle detection under `rapid`, pinned seed in PR CI, nightly randomized). The mechanism mirrors the `integration` tag: property files build only under `-tags property`; `task backend:test:property` passes rapid's seed/checks flags — legal only because the command scopes to the one package that imports rapid (an unknown `-rapid.seed` flag fails any test binary that doesn't register it).

**Files:**
- Create: `backend/internal/workflows/property_test.go` (internal: `package workflows`, `//go:build property`)
- Modify: `backend/go.mod` / `backend/go.sum` (via `go get`)
- Modify: `Taskfile.yml` (new target + tag-aware commands)
- Modify: `.golangci.yml` (build-tags)
- Modify: `backend/internal/arch/rules.go` (LoadGraph build flags)

**Interfaces:**
- Consumes: Tasks 1–4, including the unexported graph machinery (internal package).
- Produces: `task backend:test:property` honoring `RAPID_SEED` (default `1`, `0` = randomized) and `RAPID_CHECKS` (default `100`) — the nightly workflow (Task 11) invokes exactly this.

- [ ] **Step 1: Add the pinned dependency**

Run: `cd backend && go get pgregory.net/rapid@v1.3.0 && go mod tidy`
Expected: `go.mod` gains `pgregory.net/rapid v1.3.0` (no transitive additions — rapid is dependency-free).

- [ ] **Step 2: Write the property suite**

`backend/internal/workflows/property_test.go`:

```go
//go:build property

package workflows

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// idPool: small alphabet on purpose — collisions and dense graphs find
// bugs faster than sparse random strings.
var idPool = []string{"a", "b", "c", "d", "e", "f", "g", "h"}

var typePool = []NodeType{NodePlan, NodeImplement, NodeReview, NodeGate, NodeGitPublish, NodeType("bogus")}

// genArbitraryDocument draws documents with NO validity guarantees:
// duplicate ids, unknown types, dangling edges, missing config — the
// validator must classify all of it without panicking.
func genArbitraryDocument(t *rapid.T) Document {
	nNodes := rapid.IntRange(0, 8).Draw(t, "nNodes")
	d := Document{Schema: SchemaVersion}
	for i := 0; i < nNodes; i++ {
		d.Nodes = append(d.Nodes, Node{
			ID:           rapid.SampledFrom(idPool).Draw(t, "id"),
			Type:         rapid.SampledFrom(typePool).Draw(t, "type"),
			Harness:      rapid.SampledFrom([]string{"", "cc", " padded "}).Draw(t, "harness"),
			Instructions: rapid.SampledFrom([]string{"", "do the thing"}).Draw(t, "instructions"),
		})
		if rapid.Bool().Draw(t, "revision") {
			d.Nodes[i].OnRequestChanges = &RevisionTransition{
				Target:    rapid.SampledFrom(idPool).Draw(t, "target"),
				MaxRounds: rapid.IntRange(-1, 12).Draw(t, "rounds"),
			}
		}
	}
	nEdges := rapid.IntRange(0, 12).Draw(t, "nEdges")
	arts := []ArtifactType{ArtifactPlan, ArtifactPatch, ArtifactReviewVerdict, ArtifactType("blob")}
	for i := 0; i < nEdges; i++ {
		d.Edges = append(d.Edges, Edge{
			From:     rapid.SampledFrom(idPool).Draw(t, "from"),
			To:       rapid.SampledFrom(idPool).Draw(t, "to"),
			Artifact: rapid.SampledFrom(arts).Draw(t, "artifact"),
		})
	}
	return d
}

func TestPropValidateIsTotalAndDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := genArbitraryDocument(t)
		first := Validate(d)
		second := Validate(d)
		if diff := cmp.Diff(first, second); diff != "" {
			t.Fatalf("Validate is nondeterministic:\n%s", diff)
		}
	})
}

// genForwardGraph draws a DAG by construction: unique ids n0..nk, edges
// only from lower to higher index.
func genForwardGraph(t *rapid.T, maxNodes int) Document {
	n := rapid.IntRange(2, maxNodes).Draw(t, "n")
	d := Document{Schema: SchemaVersion}
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = string(rune('a' + i))
		d.Nodes = append(d.Nodes, Node{ID: ids[i], Type: NodeImplement})
	}
	nEdges := rapid.IntRange(0, n*(n-1)/2).Draw(t, "nEdges")
	for e := 0; e < nEdges; e++ {
		i := rapid.IntRange(0, n-2).Draw(t, "i")
		j := rapid.IntRange(i+1, n-1).Draw(t, "j")
		d.Edges = append(d.Edges, Edge{From: ids[i], To: ids[j], Artifact: ArtifactPatch})
	}
	return d
}

func TestPropForwardGraphsAreAcyclicUntilClosed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := genForwardGraph(t, 8)
		g := newGraph(d)
		if stuck := g.cycle(); stuck != nil {
			t.Fatalf("forward-edge graph reported cyclic: %v", stuck)
		}
		if len(d.Edges) == 0 {
			return
		}
		// Close any existing edge into a 2-cycle: must be detected.
		e := rapid.SampledFrom(d.Edges).Draw(t, "edge")
		d.Edges = append(d.Edges, Edge{From: e.To, To: e.From, Artifact: ArtifactPatch})
		if stuck := newGraph(d).cycle(); stuck == nil {
			t.Fatalf("closed edge %s↔%s not detected as a cycle", e.From, e.To)
		}
	})
}

// dominatorsBrute enumerates every root→n path (graphs are ≤7 nodes) and
// intersects them — the definition of dominance, computed the slow way.
func dominatorsBrute(g *graph, target string) map[string]bool {
	var paths [][]string
	var dfs func(id string, path []string)
	dfs = func(id string, path []string) {
		path = append(path, id)
		if id == target {
			paths = append(paths, append([]string(nil), path...))
			return
		}
		for _, e := range g.out[id] {
			dfs(e.To, path)
		}
	}
	for _, n := range g.d.Nodes {
		if len(g.in[n.ID]) == 0 {
			dfs(n.ID, nil)
		}
	}
	dom := map[string]bool{}
	if len(paths) == 0 {
		return map[string]bool{target: true}
	}
	for _, cand := range g.d.Nodes {
		onAll := true
		for _, p := range paths {
			found := false
			for _, id := range p {
				if id == cand.ID {
					found = true
					break
				}
			}
			if !found {
				onAll = false
				break
			}
		}
		if onAll {
			dom[cand.ID] = true
		}
	}
	return dom
}

func TestPropDominatorsMatchPathEnumeration(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := newGraph(genForwardGraph(t, 7))
		if g.cycle() != nil {
			t.Fatal("forward graph must be acyclic")
		}
		dom := g.dominators()
		for _, n := range g.d.Nodes {
			if diff := cmp.Diff(dominatorsBrute(g, n.ID), dom[n.ID]); diff != "" {
				t.Fatalf("dominators(%s) disagree with path enumeration:\n%s", n.ID, diff)
			}
		}
	})
}

// genConveyor draws a valid linear pipeline: optional plan → implement
// chain → optional review → gate (optional revision to a dominating
// agentic ancestor) → publish.
func genConveyor(t *rapid.T) Document {
	d := Document{Schema: SchemaVersion}
	prevPatch := "" // id of the latest patch producer
	// targets collects legal revision targets: the agentic nodes on the
	// single spine, which all dominate the gate. review is NOT one — the
	// prevPatch→gate edge bypasses it.
	var targets []string

	if rapid.Bool().Draw(t, "withPlan") {
		d.Nodes = append(d.Nodes, Node{ID: "plan", Type: NodePlan, Harness: "cc", Instructions: "p"})
		targets = append(targets, "plan")
	}
	chain := rapid.IntRange(1, 4).Draw(t, "chain")
	for i := 0; i < chain; i++ {
		id := "impl" + string(rune('0'+i))
		d.Nodes = append(d.Nodes, Node{ID: id, Type: NodeImplement, Harness: "cc", Instructions: "i"})
		if i == 0 && len(targets) > 0 {
			d.Edges = append(d.Edges, Edge{From: "plan", To: id, Artifact: ArtifactPlan})
		}
		if prevPatch != "" {
			d.Edges = append(d.Edges, Edge{From: prevPatch, To: id, Artifact: ArtifactPatch})
		}
		prevPatch = id
		targets = append(targets, id)
	}
	withReview := rapid.Bool().Draw(t, "withReview")
	if withReview {
		d.Nodes = append(d.Nodes, Node{ID: "review", Type: NodeReview, Harness: "cc", Instructions: "r"})
		d.Edges = append(d.Edges, Edge{From: prevPatch, To: "review", Artifact: ArtifactPatch})
	}
	gate := Node{ID: "gate", Type: NodeGate}
	if rapid.Bool().Draw(t, "withRevision") {
		gate.OnRequestChanges = &RevisionTransition{
			Target:    rapid.SampledFrom(targets).Draw(t, "target"),
			MaxRounds: rapid.IntRange(1, 10).Draw(t, "rounds"),
		}
	}
	d.Nodes = append(d.Nodes, gate)
	d.Edges = append(d.Edges, Edge{From: prevPatch, To: "gate", Artifact: ArtifactPatch})
	if withReview {
		d.Edges = append(d.Edges, Edge{From: "review", To: "gate", Artifact: ArtifactReviewVerdict})
	}
	d.Nodes = append(d.Nodes, Node{ID: "publish", Type: NodeGitPublish})
	d.Edges = append(d.Edges, Edge{From: "gate", To: "publish", Artifact: ArtifactPatch})
	return d
}

func TestPropLinearConveyorsAlwaysValidate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := genConveyor(t)
		if vs := Validate(d); len(vs) != 0 {
			t.Fatalf("valid conveyor rejected: %+v\ndoc: %s", vs, d.Canonical())
		}
	})
}

func TestPropCanonicalIsAFixedPointWithStableHash(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := genConveyor(t)
		re, err := ParseDocument(d.Canonical())
		if err != nil {
			t.Fatalf("reparse of canonical bytes: %v", err)
		}
		if diff := cmp.Diff(d, re); diff != "" {
			t.Fatalf("canonical round trip changed the document:\n%s", diff)
		}
		if d.Hash() != re.Hash() {
			t.Fatal("hash unstable across canonical round trip")
		}
	})
}
```

- [ ] **Step 3: Wire the `property` tag through the toolchain**

`Taskfile.yml` — add the target (next to `backend:test:integration`) and extend the tag-aware commands:

```yaml
  backend:test:property:
    desc: "Property tests (rapid): pinned seed by default; RAPID_SEED=0 randomizes (nightly)"
    dir: backend
    cmds:
      - go test -race -tags property -count=1 ./internal/workflows/ -rapid.seed={{.RAPID_SEED | default "1"}} -rapid.checks={{.RAPID_CHECKS | default "100"}}
```

Change these existing commands to carry both tags:

- `backend:vet`: `go vet -tags integration,property ./...`
- `backend:vuln`: `bin/govulncheck -tags integration,property ./...`
- `backend:analyze` (second command): `go vet -tags integration,property -vettool=./bin/caesar-analyze ./...`

And in `backend:ci`, add the property lane right after `backend:test`:

```yaml
      - task: backend:test
      - task: backend:test:property
      - task: backend:test:integration
```

`.golangci.yml` — `run.build-tags` becomes:

```yaml
run:
  timeout: 5m
  build-tags:
    - integration
    - property
```

`backend/internal/arch/rules.go` — `LoadGraph`'s config line becomes (the doc comment already says "including integration-tagged files"; extend it to "integration- and property-tagged files"):

```go
		BuildFlags: []string{"-tags", "integration,property"},
```

- [ ] **Step 4: Run the property suite, pinned and randomized**

Run: `task backend:test:property`
Expected: PASS (deterministic — run it twice, identical output).

Run: `task backend:test:property RAPID_SEED=0 RAPID_CHECKS=500`
Expected: PASS (randomized shake-out before commit).

- [ ] **Step 5: Run the repo gates**

Run: `task backend:fmt && task backend:vet && task backend:lint && task backend:analyze && task backend:test`
Expected: clean — in particular the arch tests still pass with the new build flags and the deny-list has no opinion on `pgregory.net/rapid`.

- [ ] **Step 6: Commit**

```bash
git add backend/go.mod backend/go.sum backend/internal/workflows backend/internal/arch Taskfile.yml .golangci.yml
git commit -m "test(backend): rapid property suite for the definition validator"
```

---

### Task 6: Fuzz targets for parse + validate

Fuzz coverage begins (spec §7: definition parser/validator with committed corpora). The target chains parse → canonical-fixed-point → validate, so one fuzzer covers the whole document pipeline. The committed corpus is the `f.Add` seed set (in git, run on every ordinary `go test`); the fuzzing engine itself runs nightly (Task 11) via `task backend:fuzz`. The file is **untagged** — its seed corpus is cheap and belongs in every test run.

**Files:**
- Create: `backend/internal/workflows/definition_fuzz_test.go`
- Modify: `Taskfile.yml` (`backend:fuzz` target)

**Interfaces:**
- Consumes: `ParseDocument`, `Validate`, `Canonical`, `Hash` (Tasks 1–4); `validDoc` from `definition_test.go` (same external test package).
- Produces: `task backend:fuzz` honoring `FUZZTIME` (default `2m`) — the nightly workflow invokes it.

- [ ] **Step 1: Write the fuzz target**

`backend/internal/workflows/definition_fuzz_test.go`:

```go
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
```

- [ ] **Step 2: Run the seed corpus as ordinary tests**

Run: `cd backend && go test -race ./internal/workflows/`
Expected: PASS (`FuzzParseDocument` runs its seeds as unit cases).

- [ ] **Step 3: Add the Taskfile target and smoke the fuzzer**

`Taskfile.yml`:

```yaml
  backend:fuzz:
    desc: "Run the fuzz targets for FUZZTIME each (nightly; default 2m)"
    dir: backend
    cmds:
      - go test -run '^$' -fuzz '^FuzzParseDocument$' -fuzztime {{.FUZZTIME | default "2m"}} ./internal/workflows/
```

Run: `task backend:fuzz FUZZTIME=60s`
Expected: 60 seconds of fuzzing, zero crashers. If the fuzzer finds one, it writes the input to `backend/internal/workflows/testdata/fuzz/` — fix the bug and **commit the crasher file with the fix** (that is how the committed corpus grows).

- [ ] **Step 4: Commit**

```bash
git add backend/internal/workflows Taskfile.yml
git commit -m "test(backend): fuzz target for definition parse and validate"
```

---

### Task 7: Workflow schema, migration, and sqlc store

Two tables: `workflows` (the named family, mutable metadata) and `workflow_versions` (immutable content-hashed documents — inserted, never updated). Version numbers are per-workflow monotonic; `(workflow_id, content_hash)` is unique so the idempotent write door is DB-enforced, not convention.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql`
- Create: `backend/internal/platform/pg/migrations/<timestamp>_workflow_definitions.sql` (via `task db:new`)
- Create: `backend/internal/workflows/store/queries.sql`
- Create: `backend/internal/workflows/store/{db.go,models.go,queries.sql.go}` (generated)
- Modify: `backend/sqlc.yaml` (fourth generation block)
- Modify: `Taskfile.yml` (`contracts:check` drift path list)

**Interfaces:**
- Consumes: migration/sqlc machinery from Plans 01–03.
- Produces: `store.New(db)` with `CreateWorkflow`, `ListWorkflows`, `GetWorkflow`, `GetWorkflowForUpdate`, `UpdateWorkflow`, `DeleteWorkflow` (`:execrows`), `GetVersionByHash`, `NextVersionNumber`, `InsertVersion`, `ListVersions`, `GetVersion`. `version` maps to `int32`; `document jsonb` maps to `[]byte`.

- [ ] **Step 1: Extend the desired-state schema**

Append to `backend/internal/platform/pg/schema.sql`:

```sql
-- Workflow definitions (spec §2). Plan 04: a workflow names a family of
-- immutable, content-hashed versions; the API write door validates every
-- document before insert, and version rows are never updated. Plan 05
-- will reference workflow_versions (id) from runs with ON DELETE
-- RESTRICT; until then deleting a workflow cascades its versions.
CREATE TABLE workflows (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- content_hash = hex SHA-256 of the canonical document bytes (computed
-- app-side from the parsed struct — jsonb storage re-normalizes bytes,
-- so the hash is never recomputed from this column directly).
CREATE TABLE workflow_versions (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    workflow_id  uuid NOT NULL REFERENCES workflows (id) ON DELETE CASCADE,
    version      integer NOT NULL,
    content_hash text NOT NULL,
    document     jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workflow_id, version),
    UNIQUE (workflow_id, content_hash)
);
```

- [ ] **Step 2: Author the migration**

Run: `task db:new -- workflow_definitions`
Expected: a new goose migration under `backend/internal/platform/pg/migrations/` creating both tables with both unique constraints, plus an updated `atlas.sum`. Inspect the `+goose Down` section: it must drop `workflow_versions` before `workflows`.

- [ ] **Step 3: Verify migration up-down**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/platform/pg/`
Expected: PASS — the up-down walker picks the new migration automatically.

- [ ] **Step 4: Write the queries**

`backend/internal/workflows/store/queries.sql`:

```sql
-- name: CreateWorkflow :one
INSERT INTO workflows (name, description)
VALUES ($1, $2)
RETURNING *;

-- name: GetWorkflow :one
SELECT w.id, w.name, w.description, w.created_at, w.updated_at,
       COALESCE(MAX(v.version), 0)::int AS latest_version
FROM workflows w
LEFT JOIN workflow_versions v ON v.workflow_id = w.id
WHERE w.id = $1
GROUP BY w.id;

-- name: ListWorkflows :many
SELECT w.id, w.name, w.description, w.created_at, w.updated_at,
       COALESCE(MAX(v.version), 0)::int AS latest_version
FROM workflows w
LEFT JOIN workflow_versions v ON v.workflow_id = w.id
GROUP BY w.id
ORDER BY w.name;

-- name: GetWorkflowForUpdate :one
SELECT id, name, description, created_at, updated_at
FROM workflows
WHERE id = $1
FOR UPDATE;

-- name: UpdateWorkflow :one
UPDATE workflows
SET name = $2, description = $3, updated_at = $4
WHERE id = $1
RETURNING *;

-- name: DeleteWorkflow :execrows
DELETE FROM workflows WHERE id = $1;

-- name: GetVersionByHash :one
SELECT * FROM workflow_versions
WHERE workflow_id = $1 AND content_hash = $2;

-- name: NextVersionNumber :one
SELECT (COALESCE(MAX(version), 0) + 1)::int AS next_version
FROM workflow_versions
WHERE workflow_id = $1;

-- name: InsertVersion :one
INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListVersions :many
SELECT id, workflow_id, version, content_hash, created_at
FROM workflow_versions
WHERE workflow_id = $1
ORDER BY version DESC;

-- name: GetVersion :one
SELECT * FROM workflow_versions
WHERE workflow_id = $1 AND version = $2;
```

- [ ] **Step 5: Add the sqlc generation block and generate**

In `backend/sqlc.yaml`, append a fourth entry to `sql:` — identical to the assets block except for the two paths:

```yaml
  - engine: postgresql
    schema: internal/platform/pg/schema.sql
    queries: internal/workflows/store/queries.sql
    gen:
      go:
        package: store
        out: internal/workflows/store
        sql_package: pgx/v5
        omit_unused_structs: true
        emit_json_tags: false
        overrides:
          - db_type: uuid
            go_type:
              import: github.com/google/uuid
              type: UUID
          - db_type: uuid
            nullable: true
            go_type:
              import: github.com/google/uuid
              type: NullUUID
          - db_type: timestamptz
            go_type:
              import: time
              type: Time
          - db_type: timestamptz
            nullable: true
            go_type:
              import: time
              type: Time
              pointer: true
```

Run: `task db:sqlc`
Expected: `backend/internal/workflows/store/{db.go,models.go,queries.sql.go}` generated; `version` fields are `int32`, `Document` is `[]byte`.

- [ ] **Step 6: Grow the drift gate**

In `Taskfile.yml`, `contracts:check`'s diff command gains the new store path:

```yaml
      - git diff --exit-code -- backend/api/openapi.json web/src/api backend/internal/identity/store backend/internal/platform/audit backend/internal/assets/store backend/internal/workflows/store
```

Run: `task contracts:check`
Expected: green (everything just regenerated; the openapi/ts artifacts are untouched until Task 9).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/platform/pg backend/internal/workflows/store backend/sqlc.yaml Taskfile.yml
git commit -m "feat(backend): workflow schema, migration, and sqlc store"
```

---

### Task 8: Workflows service — CRUD and the version write door

The service owns its transactions end to end (`pg.WithTx`), audits every mutation, and hosts the **single write door**: `CreateVersion` = parse → validate → canonicalize → hash → lock the workflow row → idempotent insert. No other code path may ever persist a definition (spec §4: validation lives on the write path only, and there is exactly one).

**Files:**
- Create: `backend/internal/workflows/service.go`
- Create: `backend/internal/workflows/workflows.go`
- Create: `backend/internal/workflows/testutil_integration_test.go`
- Create: `backend/internal/workflows/workflows_integration_test.go`

**Interfaces:**
- Consumes: Tasks 1–4 + 7; `platform/{pg,audit,authn,clock,httpx,xerrors}`.
- Produces (the API layer and Plan 05 consume these):
  - `workflows.NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder) *Service`
  - `workflows.Workflow{ID uuid.UUID; Name, Description string; LatestVersion int32; CreatedAt, UpdatedAt time.Time}`
  - `workflows.Version{ID uuid.UUID; WorkflowID uuid.UUID; Version int32; ContentHash string; Document []byte; CreatedAt time.Time}` (`Document` nil in list results)
  - `CreateWorkflow(ctx, actor, name, description) (Workflow, error)` · `ListWorkflows(ctx) ([]Workflow, error)` · `GetWorkflow(ctx, id) (Workflow, error)` · `UpdateWorkflow(ctx, actor, id, WorkflowUpdate{Name, Description *string}) (Workflow, error)` · `DeleteWorkflow(ctx, actor, id) error`
  - `CreateVersion(ctx, actor, workflowID, raw []byte) (Version, bool, error)` — bool = newly created · `ListVersions(ctx, workflowID) ([]Version, error)` · `GetVersion(ctx, workflowID, version int32) (Version, error)`

- [ ] **Step 1: Write the failing integration tests**

`backend/internal/workflows/testutil_integration_test.go`:

```go
//go:build integration

package workflows_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

func newTestService(t *testing.T) (*workflows.Service, *pgxpool.Pool, *clock.Fake) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	return workflows.NewService(pool, fake, audit.NewRecorder(fake)), pool, fake
}

// seedActor inserts a user row directly (workflows never imports
// identity) and returns a principal for it.
func seedActor(t *testing.T, pool *pgxpool.Pool, email string, role authn.Role) authn.Principal {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', $2) RETURNING id",
		email, string(role)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return authn.Principal{UserID: id, Email: email, Role: role}
}

func auditCount(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_entries WHERE action = $1", action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// docWith returns a valid single-node document whose instructions make
// its content (and hash) unique.
func docWith(instructions string) []byte {
	return []byte(`{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "harness": "cc", "instructions": "` + instructions + `"}]}`)
}
```

`backend/internal/workflows/workflows_integration_test.go`:

```go
//go:build integration

package workflows_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

func TestCreateWorkflow_PersistsAndAudits(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)

	w, err := svc.CreateWorkflow(ctx, admin, "conveyor", "the month-one loop")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetWorkflow(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "conveyor" || got.LatestVersion != 0 {
		t.Fatalf("got %+v", got)
	}
	if n := auditCount(t, pool, "workflow.created"); n != 1 {
		t.Fatalf("workflow.created audited %d times", n)
	}

	if _, err := svc.CreateWorkflow(ctx, admin, "conveyor", ""); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := svc.CreateWorkflow(ctx, admin, "", ""); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("empty name: %v", err)
	}
}

func TestCreateVersion_WriteDoorValidatesNumbersAndDedupes(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, err := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	if err != nil {
		t.Fatal(err)
	}

	v1, created, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first"))
	if err != nil || !created || v1.Version != 1 {
		t.Fatalf("v1: %+v created=%v err=%v", v1, created, err)
	}
	v2, created, err := svc.CreateVersion(ctx, admin, w.ID, docWith("second"))
	if err != nil || !created || v2.Version != 2 {
		t.Fatalf("v2: %+v created=%v err=%v", v2, created, err)
	}

	// Same content again — idempotent, no new row, no new audit entry.
	again, created, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first"))
	if err != nil || created || again.Version != 1 || again.ContentHash != v1.ContentHash {
		t.Fatalf("idempotent post: %+v created=%v err=%v", again, created, err)
	}
	if n := auditCount(t, pool, "workflow.version_created"); n != 2 {
		t.Fatalf("version_created audited %d times, want 2", n)
	}

	// Invalid document → classified, field-addressed.
	var xe *xerrors.Error
	_, _, err = svc.CreateVersion(ctx, admin, w.ID,
		[]byte(`{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "instructions": "x"}]}`))
	if !errors.As(err, &xe) || xe.Class != xerrors.ClassInvalid {
		t.Fatalf("invalid doc: %v", err)
	}
	found := false
	for _, f := range xe.Fields {
		if f.Field == "nodes[0].harness" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violation fields missing nodes[0].harness: %+v", xe.Fields)
	}

	if _, _, err := svc.CreateVersion(ctx, admin, w.ID, []byte(`not json`)); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("malformed json: %v", err)
	}
	ghost := seedActor(t, pool, "x@example.com", authn.RoleAdmin)
	if _, _, err := svc.CreateVersion(ctx, ghost, ghost.UserID, docWith("first")); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("unknown workflow: %v", err)
	}
}

func TestCreateVersion_ConcurrentWritersNeverShareANumber(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, err := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	if err != nil {
		t.Fatal(err)
	}

	const writers = 4
	versions := make([]int32, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith(string(rune('a'+i))))
			if err != nil {
				t.Error(err)
				return
			}
			versions[i] = v.Version
		}(i)
	}
	wg.Wait()
	seen := map[int32]bool{}
	for _, v := range versions {
		if v < 1 || v > writers || seen[v] {
			t.Fatalf("version numbers not unique/contiguous: %v", versions)
		}
		seen[v] = true
	}
}

func TestGetVersion_HashRecomputesFromStoredDocument(t *testing.T) {
	// jsonb storage re-normalizes bytes; the pinned invariant is that
	// parse(stored document) still hashes to the stored content_hash.
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, _ := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	made, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetVersion(ctx, w.ID, made.Version)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := workflows.ParseDocument(got.Document)
	if err != nil {
		t.Fatalf("stored document failed to parse: %v", err)
	}
	if doc.Hash() != got.ContentHash {
		t.Fatalf("recomputed hash %s != stored %s", doc.Hash(), got.ContentHash)
	}
	if _, err := svc.GetVersion(ctx, w.ID, 99); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("unknown version: %v", err)
	}
}

func TestListVersions_MetadataOnlyNewestFirst(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, _ := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	if _, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("second")); err != nil {
		t.Fatal(err)
	}

	vs, err := svc.ListVersions(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].Version != 2 || vs[1].Version != 1 {
		t.Fatalf("got %+v", vs)
	}
	if vs[0].Document != nil {
		t.Fatal("list results must not carry documents")
	}
	if lst, err := svc.ListWorkflows(ctx); err != nil || len(lst) != 1 || lst[0].LatestVersion != 2 {
		t.Fatalf("ListWorkflows: %+v err=%v", lst, err)
	}
}

func TestUpdateAndDeleteWorkflow_CascadeAndAudit(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, _ := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	if _, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first")); err != nil {
		t.Fatal(err)
	}

	name := "conveyor-v2"
	upd, err := svc.UpdateWorkflow(ctx, admin, w.ID, workflows.WorkflowUpdate{Name: &name})
	if err != nil || upd.Name != "conveyor-v2" {
		t.Fatalf("update: %+v err=%v", upd, err)
	}
	if err := svc.DeleteWorkflow(ctx, admin, w.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM workflow_versions").Scan(&left); err != nil || left != 0 {
		t.Fatalf("versions left after cascade: %d err=%v", left, err)
	}
	for _, action := range []string{"workflow.updated", "workflow.deleted"} {
		if n := auditCount(t, pool, action); n != 1 {
			t.Fatalf("%s audited %d times", action, n)
		}
	}
	if err := svc.DeleteWorkflow(ctx, admin, w.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("second delete: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/workflows/`
Expected: FAIL — `NewService` undefined.

- [ ] **Step 3: Implement the service**

`backend/internal/workflows/service.go`:

```go
package workflows

import (
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
)

type Service struct {
	pool  *pgxpool.Pool
	clock clock.Clock
	audit *audit.Recorder
}

func NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder) *Service {
	return &Service{pool: pool, clock: c, audit: rec}
}

func actorID(p authn.Principal) uuid.NullUUID {
	return uuid.NullUUID{UUID: p.UserID, Valid: true}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

`backend/internal/workflows/workflows.go`:

```go
package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/workflows/store"
)

// Workflow names a family of immutable definition versions (spec §2).
type Workflow struct {
	ID            uuid.UUID
	Name          string
	Description   string
	LatestVersion int32
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Version is one immutable, content-hashed definition. Document holds
// the stored JSON (nil in list results).
type Version struct {
	ID          uuid.UUID
	WorkflowID  uuid.UUID
	Version     int32
	ContentHash string
	Document    []byte
	CreatedAt   time.Time
}

// WorkflowUpdate is a partial update; nil fields are left unchanged.
type WorkflowUpdate struct {
	Name        *string
	Description *string
}

// Validation pins (plan header; byte counts, house convention).
const (
	maxWorkflowNameLen = 100
	maxWorkflowDescLen = 500
)

func validateWorkflowName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxWorkflowNameLen {
		return "", xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "name", Message: fmt.Sprintf("must be 1-%d characters", maxWorkflowNameLen),
		})
	}
	return name, nil
}

func validateWorkflowDescription(desc string) error {
	if len(desc) > maxWorkflowDescLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "description", Message: fmt.Sprintf("must be at most %d characters", maxWorkflowDescLen),
		})
	}
	return nil
}

func workflowFromRow(id uuid.UUID, name, description string, createdAt, updatedAt time.Time, latest int32) Workflow {
	return Workflow{ID: id, Name: name, Description: description, LatestVersion: latest, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

func versionFromRow(r store.WorkflowVersion) Version {
	return Version{ID: r.ID, WorkflowID: r.WorkflowID, Version: r.Version, ContentHash: r.ContentHash, Document: r.Document, CreatedAt: r.CreatedAt}
}

func (s *Service) CreateWorkflow(ctx context.Context, actor authn.Principal, name, description string) (Workflow, error) {
	name, err := validateWorkflowName(name)
	if err != nil {
		return Workflow{}, err
	}
	if err := validateWorkflowDescription(description); err != nil {
		return Workflow{}, err
	}
	var w Workflow
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).CreateWorkflow(ctx, store.CreateWorkflowParams{Name: name, Description: description})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "workflow name already taken")
		}
		if err != nil {
			return fmt.Errorf("workflows: create: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "workflow.created", SubjectKind: "workflow", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		w = workflowFromRow(row.ID, row.Name, row.Description, row.CreatedAt, row.UpdatedAt, 0)
		return nil
	})
	if err != nil {
		return Workflow{}, err
	}
	return w, nil
}

func (s *Service) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	rows, err := store.New(s.pool).ListWorkflows(ctx)
	if err != nil {
		return nil, fmt.Errorf("workflows: list: %w", err)
	}
	out := make([]Workflow, 0, len(rows))
	for _, r := range rows {
		out = append(out, workflowFromRow(r.ID, r.Name, r.Description, r.CreatedAt, r.UpdatedAt, r.LatestVersion))
	}
	return out, nil
}

func (s *Service) GetWorkflow(ctx context.Context, id uuid.UUID) (Workflow, error) {
	r, err := store.New(s.pool).GetWorkflow(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workflow{}, xerrors.E(xerrors.ClassNotFound, "no such workflow")
	}
	if err != nil {
		return Workflow{}, fmt.Errorf("workflows: get: %w", err)
	}
	return workflowFromRow(r.ID, r.Name, r.Description, r.CreatedAt, r.UpdatedAt, r.LatestVersion), nil
}

func (s *Service) UpdateWorkflow(ctx context.Context, actor authn.Principal, id uuid.UUID, upd WorkflowUpdate) (Workflow, error) {
	var w Workflow
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		current, err := q.GetWorkflowForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such workflow")
		}
		if err != nil {
			return fmt.Errorf("workflows: get for update: %w", err)
		}
		name, description := current.Name, current.Description
		if upd.Name != nil {
			if name, err = validateWorkflowName(*upd.Name); err != nil {
				return err
			}
		}
		if upd.Description != nil {
			if err := validateWorkflowDescription(*upd.Description); err != nil {
				return err
			}
			description = *upd.Description
		}
		row, err := q.UpdateWorkflow(ctx, store.UpdateWorkflowParams{ID: id, Name: name, Description: description, UpdatedAt: s.clock.Now()})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "workflow name already taken")
		}
		if err != nil {
			return fmt.Errorf("workflows: update: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "workflow.updated", SubjectKind: "workflow", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		w = workflowFromRow(row.ID, row.Name, row.Description, row.CreatedAt, row.UpdatedAt, 0)
		return nil
	})
	if err != nil {
		return Workflow{}, err
	}
	// LatestVersion is derived; refetch keeps the response honest.
	return s.GetWorkflow(ctx, w.ID)
}

func (s *Service) DeleteWorkflow(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteWorkflow(ctx, id)
		if err != nil {
			return fmt.Errorf("workflows: delete: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such workflow")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "workflow.deleted", SubjectKind: "workflow", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}

// CreateVersion is the single write door (spec §4): strict parse, full
// §2 validation, canonicalize, hash, then an insert serialized on the
// workflow row lock. Posting content that already exists for the
// workflow returns the existing version with created=false — the door
// is idempotent on content (plan header, decision 6).
func (s *Service) CreateVersion(ctx context.Context, actor authn.Principal, workflowID uuid.UUID, raw []byte) (Version, bool, error) {
	doc, err := ParseDocument(raw)
	if err != nil {
		return Version{}, false, err
	}
	if violations := Validate(doc); len(violations) > 0 {
		e := xerrors.E(xerrors.ClassInvalid, "definition failed validation")
		for _, v := range violations {
			e = e.WithFields(xerrors.FieldError{Field: v.Path, Message: v.Message})
		}
		return Version{}, false, e
	}
	canonical, hash := doc.Canonical(), doc.Hash()

	var out Version
	created := false
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.GetWorkflowForUpdate(ctx, workflowID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return xerrors.E(xerrors.ClassNotFound, "no such workflow")
			}
			return fmt.Errorf("workflows: lock workflow: %w", err)
		}
		existing, err := q.GetVersionByHash(ctx, store.GetVersionByHashParams{WorkflowID: workflowID, ContentHash: hash})
		if err == nil {
			out = versionFromRow(existing)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("workflows: version by hash: %w", err)
		}
		next, err := q.NextVersionNumber(ctx, workflowID)
		if err != nil {
			return fmt.Errorf("workflows: next version: %w", err)
		}
		row, err := q.InsertVersion(ctx, store.InsertVersionParams{
			WorkflowID: workflowID, Version: next, ContentHash: hash, Document: canonical,
		})
		if err != nil {
			return fmt.Errorf("workflows: insert version: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "workflow.version_created", SubjectKind: "workflow_version", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"workflow_id": workflowID.String(), "version": next, "content_hash": hash},
		}); err != nil {
			return err
		}
		out = versionFromRow(row)
		created = true
		return nil
	})
	if err != nil {
		return Version{}, false, err
	}
	return out, created, nil
}

func (s *Service) ListVersions(ctx context.Context, workflowID uuid.UUID) ([]Version, error) {
	if _, err := s.GetWorkflow(ctx, workflowID); err != nil {
		return nil, err
	}
	rows, err := store.New(s.pool).ListVersions(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("workflows: list versions: %w", err)
	}
	out := make([]Version, 0, len(rows))
	for _, r := range rows {
		out = append(out, Version{ID: r.ID, WorkflowID: r.WorkflowID, Version: r.Version, ContentHash: r.ContentHash, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (s *Service) GetVersion(ctx context.Context, workflowID uuid.UUID, version int32) (Version, error) {
	r, err := store.New(s.pool).GetVersion(ctx, store.GetVersionParams{WorkflowID: workflowID, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	if err != nil {
		return Version{}, fmt.Errorf("workflows: get version: %w", err)
	}
	return versionFromRow(r), nil
}
```

Adjust to the generated store types as sqlc actually names them (`GetWorkflowRow`/`ListWorkflowsRow` carry `LatestVersion int32`; `NextVersionNumber` returns `int32`) — the shapes above are what the Task 7 queries produce.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/workflows/`
Expected: PASS.

- [ ] **Step 5: Run the repo gates**

Run: `task backend:fmt && task backend:vet && task backend:lint && task backend:analyze && task backend:test`
Expected: clean — txboundary is satisfied (every transaction stays inside the context) and the arch tests see no cross-context import.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/workflows
git commit -m "feat(backend): workflows service with crud and the version write door"
```

---

### Task 9: Workflows HTTP API and the contracts extension

Nine endpoints under `/api/v1/workflows`; writes admin-only, reads for any authenticated user (plan header, decision 1). The API consumes only the `authn.SessionAuthenticator` port — no authorizer, no grant source (nothing here is project-scoped). The route table feeds both the mux and the OpenAPI generator; the regenerated contract artifacts land in the same commit.

**Files:**
- Create: `backend/internal/workflows/api/dto.go`
- Create: `backend/internal/workflows/api/api.go`
- Create: `backend/internal/workflows/api/api_integration_test.go`
- Modify: `backend/internal/contracts/contracts.go`
- Modify: `backend/api/openapi.json` + `web/src/api/schema.d.ts` (regenerated)

**Interfaces:**
- Consumes: Task 8's service; `authn.SessionAuthenticator`.
- Produces: `workflowsapi.New(svc *workflows.Service, auth authn.SessionAuthenticator) *API` and `workflowsapi.Operations(a *API) []httpx.Operation` — app wiring (Task 10) and `contracts.operationTables` both call these. Request/response DTO names below are what the contracts wrappers reference.

- [ ] **Step 1: Write the DTOs**

`backend/internal/workflows/api/dto.go`:

```go
// Package api is workflows' HTTP surface. DTO structs here are the
// source the OpenAPI document is generated from (Go types → contract,
// spec §4). The definition document itself rides as json.RawMessage —
// its schema is enforced by the write door, not the transport contract.
package api

import (
	"encoding/json"
	"time"

	"github.com/giiorg/caesar/backend/internal/workflows"
)

type WorkflowResponse struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	LatestVersion int       `json:"latest_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type WorkflowListResponse struct {
	Workflows []WorkflowResponse `json:"workflows"`
}

type CreateWorkflowRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type UpdateWorkflowRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

type CreateWorkflowVersionRequest struct {
	Document json.RawMessage `json:"document"`
}

type WorkflowVersionResponse struct {
	WorkflowID  string          `json:"workflow_id"`
	Version     int             `json:"version"`
	ContentHash string          `json:"content_hash"`
	Document    json.RawMessage `json:"document,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

type WorkflowVersionMetaResponse struct {
	Version     int       `json:"version"`
	ContentHash string    `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

type WorkflowVersionListResponse struct {
	Versions []WorkflowVersionMetaResponse `json:"versions"`
}

func workflowResponse(w workflows.Workflow) WorkflowResponse {
	return WorkflowResponse{
		ID: w.ID.String(), Name: w.Name, Description: w.Description,
		LatestVersion: int(w.LatestVersion), CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

func versionResponse(v workflows.Version) WorkflowVersionResponse {
	return WorkflowVersionResponse{
		WorkflowID: v.WorkflowID.String(), Version: int(v.Version), ContentHash: v.ContentHash,
		Document: json.RawMessage(v.Document), CreatedAt: v.CreatedAt,
	}
}
```

- [ ] **Step 2: Write the API**

`backend/internal/workflows/api/api.go`:

```go
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

type API struct {
	svc  *workflows.Service
	auth authn.SessionAuthenticator
}

func New(svc *workflows.Service, auth authn.SessionAuthenticator) *API {
	return &API{svc: svc, auth: auth}
}

// Operations is workflows' route table: mux registration (httpx.Mount)
// and OpenAPI generation (internal/contracts) both consume it.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/workflows", ID: "createWorkflow", Summary: "Create a workflow (admin)",
			Request: CreateWorkflowRequest{}, Response: WorkflowResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateWorkflow),
		},
		{
			Pattern: "GET /api/v1/workflows", ID: "listWorkflows", Summary: "List workflows",
			Response: WorkflowListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListWorkflows),
		},
		{
			Pattern: "GET /api/v1/workflows/{id}", ID: "getWorkflow", Summary: "Get a workflow",
			Response: WorkflowResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetWorkflow),
		},
		{
			Pattern: "PATCH /api/v1/workflows/{id}", ID: "updateWorkflow", Summary: "Update name or description (admin)",
			Request: UpdateWorkflowRequest{}, Response: WorkflowResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleUpdateWorkflow),
		},
		{
			Pattern: "DELETE /api/v1/workflows/{id}", ID: "deleteWorkflow", Summary: "Delete a workflow and its versions (admin)",
			Status:  http.StatusNoContent,
			Handler: a.adminOnly(a.handleDeleteWorkflow),
		},
		{
			// The single write door (spec §4). 201 on creation; posting
			// content that already exists returns the existing version
			// with 200 (idempotent, decision 6).
			Pattern: "POST /api/v1/workflows/{id}/versions", ID: "createWorkflowVersion", Summary: "Validate and persist an immutable definition version (admin)",
			Request: CreateWorkflowVersionRequest{}, Response: WorkflowVersionResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateWorkflowVersion),
		},
		{
			Pattern: "GET /api/v1/workflows/{id}/versions", ID: "listWorkflowVersions", Summary: "List version metadata, newest first",
			Response: WorkflowVersionListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListWorkflowVersions),
		},
		{
			Pattern: "GET /api/v1/workflows/{id}/versions/{version}", ID: "getWorkflowVersion", Summary: "Get one version with its document",
			Response: WorkflowVersionResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetWorkflowVersion),
		},
	}
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession)

// authenticated/adminOnly mirror the other API packages (contexts never
// share hand-rolled helpers across the boundary; the copies are small).
func (a *API) authenticated(next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(authn.SessionCookie)
		if err != nil {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassUnauthenticated, "authentication required"))
			return
		}
		sess, err := a.auth.AuthenticateSession(r.Context(), c.Value)
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		next(w, r, sess)
	})
}

func (a *API) adminOnly(next sessionHandler) http.Handler {
	return a.authenticated(func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
		if sess.Role != authn.RoleAdmin {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassForbidden, "admin role required"))
			return
		}
		next(w, r, sess)
	})
}

func decode[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, xerrors.Wrap(xerrors.ClassInvalid, "invalid request body", err)
	}
	return v, nil
}

func pathUUID(r *http.Request, notFound string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.UUID{}, xerrors.E(xerrors.ClassNotFound, notFound)
	}
	return id, nil
}

func pathVersion(r *http.Request) (int32, error) {
	v, err := strconv.ParseInt(r.PathValue("version"), 10, 32)
	if err != nil || v < 1 {
		return 0, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	return int32(v), nil
}

func (a *API) handleCreateWorkflow(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[CreateWorkflowRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	wf, err := a.svc.CreateWorkflow(r.Context(), sess.Principal, req.Name, req.Description)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, workflowResponse(wf))
}

func (a *API) handleListWorkflows(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	list, err := a.svc.ListWorkflows(r.Context())
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := WorkflowListResponse{Workflows: make([]WorkflowResponse, 0, len(list))}
	for _, wf := range list {
		out.Workflows = append(out.Workflows, workflowResponse(wf))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetWorkflow(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	wf, err := a.svc.GetWorkflow(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, workflowResponse(wf))
}

func (a *API) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	req, err := decode[UpdateWorkflowRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	wf, err := a.svc.UpdateWorkflow(r.Context(), sess.Principal, id, workflows.WorkflowUpdate{Name: req.Name, Description: req.Description})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, workflowResponse(wf))
}

func (a *API) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if err := a.svc.DeleteWorkflow(r.Context(), sess.Principal, id); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCreateWorkflowVersion(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	req, err := decode[CreateWorkflowVersionRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if len(req.Document) == 0 {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "document", Message: "required"}))
		return
	}
	v, created, err := a.svc.CreateVersion(r.Context(), sess.Principal, id, req.Document)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, versionResponse(v))
}

func (a *API) handleListWorkflowVersions(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	list, err := a.svc.ListVersions(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := WorkflowVersionListResponse{Versions: make([]WorkflowVersionMetaResponse, 0, len(list))}
	for _, v := range list {
		out.Versions = append(out.Versions, WorkflowVersionMetaResponse{Version: int(v.Version), ContentHash: v.ContentHash, CreatedAt: v.CreatedAt})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetWorkflowVersion(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	ver, err := pathVersion(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	v, err := a.svc.GetVersion(r.Context(), id, ver)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, versionResponse(v))
}
```

- [ ] **Step 3: Write the API integration tests**

`backend/internal/workflows/api/api_integration_test.go` — same fake-ports pattern as assets (workflows may never import identity; real wiring is exercised in Task 10's e2e):

```go
//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/workflows"
	workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"
)

type fakeAuth struct {
	sessions map[string]authn.AuthSession
}

func (f *fakeAuth) AuthenticateSession(_ context.Context, token string) (authn.AuthSession, error) {
	s, ok := f.sessions[token]
	if !ok {
		return authn.AuthSession{}, xerrors.E(xerrors.ClassUnauthenticated, "invalid or expired session")
	}
	return s, nil
}

// newTestServer seeds one admin ("admin-token") and one member
// ("member-token") and mounts the real route table over the real
// service against a real database.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	svc := workflows.NewService(pool, fake, audit.NewRecorder(fake))

	auth := &fakeAuth{sessions: map[string]authn.AuthSession{}}
	for token, role := range map[string]authn.Role{"admin-token": authn.RoleAdmin, "member-token": authn.RoleMember} {
		var id uuid.UUID
		err := pool.QueryRow(context.Background(),
			"INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', $2) RETURNING id",
			token+"@example.com", string(role)).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		auth.sessions[token] = authn.AuthSession{Principal: authn.Principal{UserID: id, Email: token + "@example.com", Role: role}, SessionID: uuid.New()}
	}

	mux := http.NewServeMux()
	httpx.Mount(mux, workflowsapi.Operations(workflowsapi.New(svc, auth)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path, token string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Cookie", authn.SessionCookie+"="+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

const testDoc = `{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "harness": "cc", "instructions": "x"}]}`

func TestWorkflowsAPI_AdminWritesMemberReads(t *testing.T) {
	srv := newTestServer(t)

	created := do(t, srv, http.MethodPost, "/api/v1/workflows", "admin-token",
		workflowsapi.CreateWorkflowRequest{Name: "conveyor"})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create workflow = %d", created.StatusCode)
	}
	var wf workflowsapi.WorkflowResponse
	if err := json.NewDecoder(created.Body).Decode(&wf); err != nil {
		t.Fatal(err)
	}

	post := do(t, srv, http.MethodPost, "/api/v1/workflows/"+wf.ID+"/versions", "admin-token",
		workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(testDoc)})
	if post.StatusCode != http.StatusCreated {
		t.Fatalf("create version = %d", post.StatusCode)
	}

	// Idempotent replay: same content → 200, same version.
	replay := do(t, srv, http.MethodPost, "/api/v1/workflows/"+wf.ID+"/versions", "admin-token",
		workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(testDoc)})
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("idempotent replay = %d, want 200", replay.StatusCode)
	}

	got := do(t, srv, http.MethodGet, "/api/v1/workflows/"+wf.ID+"/versions/1", "member-token", nil)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("member get version = %d", got.StatusCode)
	}
	var v workflowsapi.WorkflowVersionResponse
	if err := json.NewDecoder(got.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || len(v.Document) == 0 || v.ContentHash == "" {
		t.Fatalf("version response: %+v", v)
	}

	list := do(t, srv, http.MethodGet, "/api/v1/workflows", "member-token", nil)
	if list.StatusCode != http.StatusOK {
		t.Fatalf("member list = %d", list.StatusCode)
	}
}

func TestWorkflowsAPI_RBACAndAuth(t *testing.T) {
	srv := newTestServer(t)

	if resp := do(t, srv, http.MethodPost, "/api/v1/workflows", "member-token",
		workflowsapi.CreateWorkflowRequest{Name: "nope"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member create = %d, want 403", resp.StatusCode)
	}
	if resp := do(t, srv, http.MethodGet, "/api/v1/workflows", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d, want 401", resp.StatusCode)
	}
	if resp := do(t, srv, http.MethodGet, "/api/v1/workflows", "bogus", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bogus token = %d, want 401", resp.StatusCode)
	}
}

func TestWorkflowsAPI_ValidationSurfacesFieldErrors(t *testing.T) {
	srv := newTestServer(t)
	created := do(t, srv, http.MethodPost, "/api/v1/workflows", "admin-token",
		workflowsapi.CreateWorkflowRequest{Name: "conveyor"})
	var wf workflowsapi.WorkflowResponse
	if err := json.NewDecoder(created.Body).Decode(&wf); err != nil {
		t.Fatal(err)
	}

	bad := `{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "instructions": "x"}]}`
	resp := do(t, srv, http.MethodPost, "/api/v1/workflows/"+wf.ID+"/versions", "admin-token",
		workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(bad)})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid doc = %d, want 400", resp.StatusCode)
	}
	var pd httpx.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&pd); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fe := range pd.Errors {
		if fe.Field == "nodes[0].harness" {
			found = true
		}
	}
	if !found {
		t.Fatalf("problem errors missing nodes[0].harness: %+v", pd.Errors)
	}

	if resp := do(t, srv, http.MethodGet, "/api/v1/workflows/"+wf.ID+"/versions/notanumber", "member-token", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad version segment = %d, want 404", resp.StatusCode)
	}
}
```

- [ ] **Step 4: Run the API tests**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/workflows/api/`
Expected: PASS.

- [ ] **Step 5: Extend the contracts generator**

In `backend/internal/contracts/contracts.go`:

1. Import: `workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"`.
2. Wrapper structs, next to the existing ones:

```go
type idAndUpdateWorkflow struct {
	idParams
	workflowsapi.UpdateWorkflowRequest
}

type idAndCreateWorkflowVersion struct {
	idParams
	workflowsapi.CreateWorkflowVersionRequest
}

// workflowVersionParams covers GET …/{id}/versions/{version} — two path
// parameters, no body.
type workflowVersionParams struct {
	idParams
	Version string `path:"version" description:"version number"`
}
```

3. `requestOverrides` gains:

```go
	"updateWorkflow":         idAndUpdateWorkflow{},
	"createWorkflowVersion":  idAndCreateWorkflowVersion{},
	"getWorkflowVersion":     workflowVersionParams{},
```

4. `operationTables` gains:

```go
			workflowsapi.Operations(workflowsapi.New(nil, nil)),
```

(`listWorkflowVersions` needs no override — no body + `{id}` resolves to `idParams` automatically.)

- [ ] **Step 6: Regenerate and gate**

Run: `task contracts:gen && task contracts:check`
Expected: `backend/api/openapi.json` gains the eight workflow operations (the `document` fields reflect as free-form JSON — correct: the transport contract does not duplicate the write door's schema); `web/src/api/schema.d.ts` regenerates; the check is green.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/workflows/api backend/internal/contracts backend/api/openapi.json web/src/api/schema.d.ts
git commit -m "feat(backend): workflows http api with contracts"
```

---

### Task 10: App wiring and the e2e smoke

The real composition: `serve.go` constructs the workflows service and appends its route table, with `identity.Service` as the session authenticator — the only place real identity and workflows wiring meet. An e2e test drives the whole loop through `app.Routes`: bootstrap → login → create workflow → post version → read it back.

**Files:**
- Modify: `backend/internal/app/serve.go`
- Create: `backend/internal/app/workflows_e2e_integration_test.go`

**Interfaces:**
- Consumes: Tasks 8–9; existing app wiring.
- Produces: the running binary serves `/api/v1/workflows`.

- [ ] **Step 1: Wire serve.go**

In `backend/internal/app/serve.go`, add the imports:

```go
	"github.com/giiorg/caesar/backend/internal/workflows"
	workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"
```

After `assetsSvc` is constructed, add:

```go
	workflowsSvc := workflows.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}))
```

And extend the ops assembly (identity.Service is the session authenticator, same port wiring as assets):

```go
	ops = append(ops, workflowsapi.Operations(workflowsapi.New(workflowsSvc, svc))...)
```

- [ ] **Step 2: Write the e2e test**

`backend/internal/app/workflows_e2e_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/app"
	"github.com/giiorg/caesar/backend/internal/identity"
	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/workflows"
	workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"
)

// The definitions loop through the real Routes wiring: bootstrap →
// login → create workflow → post a version through the write door →
// read the stored document back.
func TestAppRoutes_WorkflowsEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	svc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	if _, err := svc.Bootstrap(ctx, "root@example.com", "a long enough password"); err != nil {
		t.Fatal(err)
	}
	workflowsSvc := workflows.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}))

	ops := identityapi.Operations(identityapi.New(svc, 90*24*time.Hour, nil))
	ops = append(ops, workflowsapi.Operations(workflowsapi.New(workflowsSvc, svc))...)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(app.Routes(log, pool, ops, "http://caesar.test"))
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"email": "root@example.com", "password": "a long enough password"})
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	var token string
	for _, c := range resp.Cookies() {
		if c.Name == identityapi.SessionCookie {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no session cookie")
	}

	post := func(path string, payload any) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
		req.Header.Set("Cookie", identityapi.SessionCookie+"="+token)
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Body.Close() })
		return r
	}

	created := post("/api/v1/workflows", workflowsapi.CreateWorkflowRequest{Name: "conveyor"})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create workflow = %d", created.StatusCode)
	}
	var wf workflowsapi.WorkflowResponse
	if err := json.NewDecoder(created.Body).Decode(&wf); err != nil {
		t.Fatal(err)
	}

	doc := `{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "harness": "cc", "instructions": "implement it"}]}`
	posted := post("/api/v1/workflows/"+wf.ID+"/versions", workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(doc)})
	if posted.StatusCode != http.StatusCreated {
		t.Fatalf("create version = %d", posted.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/workflows/"+wf.ID+"/versions/1", nil)
	req.Header.Set("Cookie", identityapi.SessionCookie+"="+token)
	got, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get version = %d", got.StatusCode)
	}
	var v workflowsapi.WorkflowVersionResponse
	if err := json.NewDecoder(got.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || v.ContentHash == "" || len(v.Document) == 0 {
		t.Fatalf("version = %+v", v)
	}
}
```

(`identityapi.New`'s third argument is the ProjectChecker used only by grant endpoints; `nil` is fine here because this test never touches `/grants` — same shape the identity API tests use.)

- [ ] **Step 3: Run the app tests**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/app/`
Expected: PASS, including the pre-existing e2e tests (serve wiring compiles and mounts without pattern conflicts).

- [ ] **Step 4: Run the repo gates**

Run: `task backend:fmt && task backend:vet && task backend:lint && task backend:analyze && task backend:test`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/app
git commit -m "feat(backend): wire the workflows context into serve"
```

---

### Task 11: Nightly CI — randomized properties and fuzzing

The spec §7 nightly lane: randomized rapid seeds and real fuzzing time. A separate workflow on a schedule — deliberately **not** part of the `ci-ok` required check; a nightly failure is a bug report, not a merge blocker.

**Files:**
- Create: `.github/workflows/nightly.yml`

**Interfaces:**
- Consumes: `task backend:test:property` (Task 5) and `task backend:fuzz` (Task 6).
- Produces: a scheduled + manually-dispatchable workflow.

- [ ] **Step 1: Write the workflow**

`.github/workflows/nightly.yml`:

```yaml
name: nightly

on:
  schedule:
    - cron: '30 3 * * *'
  workflow_dispatch:

permissions:
  contents: read

jobs:
  property-randomized:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: backend/go.mod
          cache-dependency-path: backend/go.sum
      - uses: arduino/setup-task@v2
        with:
          version: 3.x
          repo-token: ${{ secrets.GITHUB_TOKEN }}
      - run: task backend:test:property RAPID_SEED=0 RAPID_CHECKS=10000

  fuzz:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: backend/go.mod
          cache-dependency-path: backend/go.sum
      - uses: arduino/setup-task@v2
        with:
          version: 3.x
          repo-token: ${{ secrets.GITHUB_TOKEN }}
      - run: task backend:fuzz FUZZTIME=10m
```

- [ ] **Step 2: Lint it**

Run: `task lint:actions`
Expected: clean.

- [ ] **Step 3: Verify the dispatch path once the branch is pushed**

After the branch is pushed (PR open), trigger it manually: `gh workflow run nightly --ref <branch>` — GitHub only offers `workflow_dispatch` for workflows that exist on some pushed ref; if the run is refused before merge, note it in the PR body and verify after merge instead. Never mark this step done on a refusal alone.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/nightly.yml
git commit -m "ci(repo): nightly randomized property and fuzz lanes"
```

---

### Task 12: Docs, roadmap, final green

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`
- Commit: `docs/superpowers/plans/2026-08-03-caesar-04-workflow-definitions.md` (this document)

- [ ] **Step 1: README**

In the README's endpoint overview, add the workflows resources (`/api/v1/workflows` + `/versions` — note "writes admin-only, single write door validates every document"). In the testing section, document the two new lanes: `task backend:test:property` (rapid, `RAPID_SEED=0` to randomize) and `task backend:fuzz` (`FUZZTIME`), and that nightly CI runs both.

- [ ] **Step 2: Roadmap status**

In `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`, flip row 04 to `**done** — 2026-08-03-caesar-04-workflow-definitions.md`.

- [ ] **Step 3: Full gate**

Run: `task ci`
Expected: every lane green — backend (build, fmt, vet, lint, analyze, race, property, integration, vuln), contracts (openapi/ts/sqlc regen-diff), secrets (worktree caveat below — CI is authoritative), workflow lint.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/superpowers/plans
git commit -m "docs(repo): plan 04 recorded, readme covers workflows and test lanes"
```

---

## Execution notes

- **Worktree:** execute in an isolated worktree per superpowers:using-git-worktrees, named `caesar-04-workflow-definitions`. Branch from current `main` (`c971a13` or later).
- **Working pattern that held for Plans 02–03:** implementers haiku/sonnet, reviewers sonnet, final whole-branch review on the most capable model; **one implementer at a time** — tasks are sequential, later tasks consume earlier interfaces. Subagents sometimes go idle without sending their report; one SendMessage nudge retrieves it.
- **Integration tests** need Docker (testcontainers boots postgres:18-alpine). The dev compose project is only for running the server by hand: `docker compose -f compose.dev.yaml -p caesar-04-workflow-definitions up -d`; take it down at cleanup.
- **`task secrets` cannot run inside a linked worktree** (gitleaks' Docker mount can't resolve the external gitdir pointer — session-12 finding). Rely on CI for the secrets lane.
- **`task db:new` needs Docker** (Atlas dev database).
- **Session-11/12/15 rulings still bind:** txboundary skips generated files; nolint only with an in-code invariant comment; length counts are bytes, matching identity.
- **Codex review:** per the roadmap's rule 4, implementation-stage `code_diff` reviews run within this plan's execution (per-task reviewer + final whole-branch review), not as artifact rounds.
- **Carried forward (triaged, not this plan's scope):** UseCredential `purpose` bounds (control-plane plans), Plan 02/03 hardening list in the project memory, and — new from this plan — promoting the thrice-duplicated `authenticated`/`adminOnly` middleware into a shared helper if a fourth API package ever appears, and a validate-only (dry-run) endpoint for the editor UI when Plan 12 needs it.

