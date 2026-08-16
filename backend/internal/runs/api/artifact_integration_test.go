//go:build integration

package api_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
)

// stagedDiff is the CANONICAL patch bytes — what a human reads, and what
// collect.go base64-encodes before it ever reaches a payload column.
const stagedDiff = "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n"

// diffPayload is a patch artifact's payload in the PRODUCTION wire form
// (collect.go:145-152): diff base64, base_sha alongside it. The suite's other
// fixtures write patchPayload instead — tree_hash and base_sha, which is what
// mintAuthorization reads — because none of them projects the content. Here the
// content IS the subject, so the fixture has to write what the harness writes.
func diffPayload(baseSHA string) string {
	return fmt.Sprintf(`{"diff":%q,"base_sha":%q}`,
		base64.StdEncoding.EncodeToString([]byte(stagedDiff)), baseSHA)
}

func artifactPath(runID, artifactID uuid.UUID) string {
	return "/api/v1/runs/" + runID.String() + "/artifacts/" + artifactID.String()
}

// stageRunWithPatchArtifact mints a run in project A and writes one patch
// artifact of it. The run is left as intake created it: this endpoint reads an
// artifact of a run and nothing about it depends on the run being parked at a
// gate, so the fixture stages the two rows it actually asserts on.
func (h *harness) stageRunWithPatchArtifact(t *testing.T) (uuid.UUID, uuid.UUID) {
	t.Helper()
	task := h.createTask(t, "runner-tok", "read my patch")
	runID, err := uuid.Parse(task.LatestRunID)
	if err != nil {
		t.Fatal(err)
	}
	var baseSHA string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT base_sha FROM runs WHERE id = $1`, runID).Scan(&baseSHA); err != nil {
		t.Fatal(err)
	}
	return runID, h.stageArtifact(t, runID, "impl", "patch", 1, 1, 5, diffPayload(baseSHA))
}

func (h *harness) getArtifact(t *testing.T, token string, runID, artifactID uuid.UUID) runsapi.ArtifactPayloadResponse {
	t.Helper()
	resp := h.do(t, http.MethodGet, artifactPath(runID, artifactID), token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get artifact = %d, want 200", resp.StatusCode)
	}
	return decodeInto[runsapi.ArtifactPayloadResponse](t, resp)
}

func TestArtifactPayloadRetrieval(t *testing.T) {
	h := newHarness(t)
	run, patchID := h.stageRunWithPatchArtifact(t) // project A

	t.Run("a viewer of the run reads the payload", func(t *testing.T) {
		got := h.getArtifact(t, "viewer-tok", run, patchID)
		if got.ID != patchID.String() || got.Type != "patch" {
			t.Fatalf("got %+v", got)
		}
		if got.RunID != run.String() {
			t.Errorf("run_id = %q, want %q", got.RunID, run)
		}
		raw, ok := got.Payload["diff"].(string)
		if !ok {
			t.Fatal("payload has no diff string")
		}
		// THE CONTRACT IS BASE64 AND THIS IS WHERE IT IS PINNED. collect.go
		// encodes because a patch is arbitrary bytes: a file in a legacy
		// encoding diffs as non-UTF-8, and JSON string encoding would replace
		// those bytes with U+FFFD SILENTLY — the mangled patch still applies.
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			t.Errorf("diff is not base64: %v", err)
		}
		// Decoding to SOMETHING is not decoding to the patch. Without this an
		// endpoint that shipped any base64 string — a truncation, another
		// artifact's content — would satisfy the check above.
		if string(decoded) != stagedDiff {
			t.Errorf("decoded diff = %q, want %q", decoded, stagedDiff)
		}
	})

	t.Run("grant scoping is on the RUN, not on the artifact id", func(t *testing.T) {
		// An artifact id must not be usable to reach across a project boundary.
		resp := h.do(t, http.MethodGet, artifactPath(run, patchID), "approver-b-tok", nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("cross-project artifact read = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("an artifact of a DIFFERENT run is 404, not 200", func(t *testing.T) {
		// The path names a run; addressing another run's artifact through it
		// must not work even when the caller may see both. Without the
		// run_id check the handler is a bare id lookup and the path segment is
		// decoration.
		otherRun, _ := h.stageRunWithPatchArtifact(t)
		resp := h.do(t, http.MethodGet, artifactPath(otherRun, patchID), "approver-tok", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("artifact of another run = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("an unknown artifact id is 404 for everyone", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, artifactPath(run, uuid.New()), "approver-tok", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown artifact = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("a malformed artifact id is 404, never 500", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, "/api/v1/runs/"+run.String()+"/artifacts/not-a-uuid", "approver-tok", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("malformed artifact id = %d, want 404", resp.StatusCode)
		}
	})
}
