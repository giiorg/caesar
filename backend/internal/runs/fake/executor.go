// Package fake holds the deterministic doubles behind the spine's ports
// (spec §7 golden set, invariant 14): a scripted executor, a map-backed
// forge, and a bare-repo fixture. First-class packages so tests and
// replay tooling share them; production serve never wires fake.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

const directivePrefix = "caesar-fake:"

// directive is one parsed scripting line from node instructions.
type directive struct {
	hang    bool
	failN   int
	failOut runs.Outcome
}

// parseDirective scans instructions for the first caesar-fake: line.
func parseDirective(instructions string) (directive, error) {
	for _, line := range strings.Split(instructions, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, directivePrefix) {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, directivePrefix)))
		if len(fields) == 0 {
			return directive{}, fmt.Errorf("fake: empty directive")
		}
		switch fields[0] {
		case "hang":
			return directive{hang: true}, nil
		case "fail":
			d := directive{failOut: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeHarnessError, Message: "scripted failure"}}
			for _, kv := range fields[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return directive{}, fmt.Errorf("fake: bad directive field %q", kv)
				}
				switch k {
				case "attempts":
					n, err := strconv.Atoi(v)
					if err != nil || n < 1 {
						return directive{}, fmt.Errorf("fake: bad attempts %q", v)
					}
					d.failN = n
				case "class":
					d.failOut.Class = runs.OutcomeClass(v)
				case "code":
					d.failOut.Code = v
				default:
					return directive{}, fmt.Errorf("fake: unknown directive field %q", k)
				}
			}
			if d.failN == 0 {
				return directive{}, fmt.Errorf("fake: fail directive needs attempts=N")
			}
			return d, nil
		default:
			return directive{}, fmt.Errorf("fake: unknown directive %q", fields[0])
		}
	}
	return directive{}, nil
}

// Executor is the deterministic fake harness: scripted outputs, failure
// injection via directives, an execution registry for Inspect.
type Executor struct {
	mu        sync.Mutex
	execCount map[string]int                           // per (run,node): executions seen
	completed map[runs.AttemptRef]runs.ExecutionResult // successful executions
	// remotes maps a repo id to a local bare repository. Registering one
	// switches the implement artifact from a synthetic patch to a PUBLISHABLE
	// one — see scripted.
	remotes map[uuid.UUID]string
	// planPayload replaces the scripted plan payload wholesale; planExtra
	// merges keys into whatever plan payload is emitted. They exist so a test
	// can drive a real orchestrator with a payload of its own choosing —
	// notably a pre-contract one — without a second executor.
	planPayload map[string]any
	planExtra   map[string]any
	// lastInstructions is what Execute was HANDED, per node id. It is recorded
	// rather than reconstructed: the point of asking is to see what the runner
	// appended, and a reconstruction would be a copy of the rule under test.
	lastInstructions map[string]string
}

func NewExecutor() *Executor {
	return &Executor{
		execCount:        map[string]int{},
		completed:        map[runs.AttemptRef]runs.ExecutionResult{},
		remotes:          map[uuid.UUID]string{},
		lastInstructions: map[string]string{},
	}
}

// PlanPayload replaces the scripted plan payload with p.
func (e *Executor) PlanPayload(p map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.planPayload = p
}

// PlanExtra merges extra keys into whatever plan payload is emitted.
func (e *Executor) PlanExtra(p map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.planExtra = p
}

// LastInstructions reports the instructions Execute most recently received for
// a node id, "" if it was never executed.
func (e *Executor) LastInstructions(nodeID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastInstructions[nodeID]
}

// Remote registers the local bare repository a repo id resolves to, which is
// what lets this executor emit a patch the publish path can actually push.
func (e *Executor) Remote(repoID uuid.UUID, bare string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.remotes[repoID] = bare
}

func nodeKey(ref runs.AttemptRef) string { return ref.RunID.String() + "/" + ref.NodeID }

func (e *Executor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	// Recorded FIRST, before anything can divert this call: a hung or scripted-
	// to-fail attempt was still told something, and that is what LastInstructions
	// is asked about.
	e.mu.Lock()
	e.lastInstructions[req.Ref.NodeID] = req.Instructions
	e.mu.Unlock()

	d, err := parseDirective(req.Instructions)
	if err != nil {
		return runs.ExecutionResult{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodeInvalidOutput, Message: err.Error(),
		}}
	}
	if d.hang {
		<-ctx.Done()
		return runs.ExecutionResult{}, ctx.Err()
	}
	e.mu.Lock()
	e.execCount[nodeKey(req.Ref)]++
	n := e.execCount[nodeKey(req.Ref)]
	e.mu.Unlock()
	if d.failN > 0 && n <= d.failN {
		return runs.ExecutionResult{}, runs.OutcomeError{Outcome: d.failOut}
	}

	art, err := e.scripted(req)
	if err != nil {
		return runs.ExecutionResult{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodeInvalidOutput, Message: err.Error(),
		}}
	}
	res := runs.ExecutionResult{Artifacts: []runs.NewArtifact{art}}
	e.mu.Lock()
	e.completed[req.Ref] = res
	e.mu.Unlock()
	return res, nil
}

// scripted builds the deterministic success artifact for a node kind.
func (e *Executor) scripted(req runs.ExecutionRequest) (runs.NewArtifact, error) {
	firstLine, _, _ := strings.Cut(req.Instructions, "\n")
	switch req.Kind {
	case runs.KindImplement:
		e.mu.Lock()
		bare, hasRemote := e.remotes[req.Repo.ID]
		e.mu.Unlock()
		if !hasRemote {
			// No remote registered: the historical synthetic patch. It is not
			// publishable — the diff is raw text where the wire form is
			// base64, and the tree hash is a digest of the diff rather than a
			// git tree object id — but nothing without a remote can publish
			// anyway, and every pre-Plan-08 test asserts against this shape.
			diff := fmt.Sprintf("--- a/CAESAR.md\n+++ b/CAESAR.md\n@@ -0,0 +1 @@\n+%s (%s)\n", firstLine, req.Ref.NodeID)
			sum := sha256.Sum256([]byte(diff + req.BaseSHA))
			return runs.NewArtifact{Type: runs.ArtifactKindPatch, Payload: map[string]any{
				"diff": diff, "base_sha": req.BaseSHA, "tree_hash": hex.EncodeToString(sum[:]),
			}}, nil
		}
		// A PUBLISHABLE patch: the base64 canonical wire form, and the tree
		// hash `git write-tree` really produces after applying it at the run's
		// base. gitops.Publish refuses anything less, so a synthetic tree hash
		// would fail every publication test for a reason unrelated to what it
		// exercises.
		//
		// The file is named per node so two implement nodes in one definition
		// cannot both claim to create the same path.
		name := "CAESAR-" + req.Ref.NodeID + ".md"
		content := fmt.Sprintf("%s (%s)\n", firstLine, req.Ref.NodeID)
		tree, err := approvedTree(bare, req.BaseSHA, name, content)
		if err != nil {
			return runs.NewArtifact{}, fmt.Errorf("fake: build publishable patch: %w", err)
		}
		return runs.NewArtifact{Type: runs.ArtifactKindPatch, Payload: map[string]any{
			"diff": addFilePatch(name, content), "base_sha": req.BaseSHA, "tree_hash": tree,
		}}, nil
	case runs.KindReview:
		return runs.NewArtifact{Type: runs.ArtifactKindReviewVerdict, Payload: map[string]any{
			// `notes` is kept alongside the schema-2 keys, and NOT because
			// anything asserts on it — nothing does; this emission is its only
			// occurrence in the tree. It stays for two reasons. Deleting a key
			// the fake has always emitted is churn on a double, changing what a
			// dozen suites observe to buy nothing. And an unknown key riding
			// beside the required ones is exactly the case spec §2.3 promises to
			// accept, so carrying one makes every fake-driven run exercise that
			// promise instead of only the required set.
			//
			// The counts are untyped Go ints, which is what a payload built in
			// Go really carries when nothing marshals it on the way to the
			// orchestrator. runs.optionalNumber accepts them; converting them to
			// float64 here would be the fake pretending to have been decoded.
			"verdict":  "approve",
			"notes":    "fake review",
			"findings": []any{},
			"tests":    map[string]any{"status": "passed", "passed": 1, "failed": 0},
		}}, nil
	default: // plan and anything agentic-shaped
		// `content` is kept for the same two reasons `notes` is above — churn on
		// a double buys nothing, and an unknown key beside the required ones
		// exercises the §2.3 extra-keys promise on the plan side too — plus one
		// that is specific to it: deleting it would SILENTLY VACATE
		// executor_test.go's determinism check rather than fail it, since that
		// check compares two emissions to each other and nil == nil.
		//
		// MEASURED, not assumed. Deleting this key left every backend suite
		// green, unit and integration alike. An earlier version of this comment
		// claimed five suites would break; they do not, because the runner's
		// `content` sites script their own payloads and never run this fake.
		// executor_test.go now pins the emitted value against a literal, which
		// is what makes the first sentence true going forward.
		payload := map[string]any{
			"content": "plan: " + firstLine,
			"summary": "plan: " + firstLine,
			"steps":   []any{map[string]any{"title": firstLine}},
		}
		e.mu.Lock()
		if e.planPayload != nil {
			payload = maps.Clone(e.planPayload)
		}
		maps.Copy(payload, e.planExtra)
		e.mu.Unlock()
		return runs.NewArtifact{Type: runs.ArtifactKindPlan, Payload: payload}, nil
	}
}

func (e *Executor) Inspect(_ context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	res, ok := e.completed[ref]
	if !ok {
		return runs.InspectResult{}, nil
	}
	return runs.InspectResult{Completed: true, Result: res}, nil
}

// ReconcileCredential is the no-op half of runs.CredentialReconciler: the fake
// harness holds no credentials, so an attempt of its has no obligation to
// settle. It exists so the janitor paths that type-assert the executor exercise
// their real ordering in fake-driven tests rather than skipping silently.
func (e *Executor) ReconcileCredential(context.Context, runs.AttemptRef) error { return nil }

// Completed reports whether ref finished successfully (test assertions).
func (e *Executor) Completed(ref runs.AttemptRef) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.completed[ref]
	return ok
}
