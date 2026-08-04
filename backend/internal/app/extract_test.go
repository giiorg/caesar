package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// The patch is arbitrary bytes: a text file in a legacy encoding is diffed as
// text, so raw non-UTF-8 bytes land in it. Carrying it as a JSON string would
// swap them for U+FFFD, and the mangled patch would still apply — a run
// reported successful that does not reproduce the agent's work. This pins the
// report against exactly that, through the real contract-file helpers.
func TestExtractReport_NonUTF8PatchSurvivesTheContractFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extract.report.json")
	patch := []byte("--- a/latin1.txt\n+++ b/latin1.txt\n@@ -0,0 +1 @@\n+caf\xe9 na\xefve\n")
	sent := gitops.ExtractReport{
		BaseSHA:  strings.Repeat("a", 40),
		TreeHash: strings.Repeat("b", 40),
		Changed:  true,
		Diff:     patch,
	}
	if err := writeJSONFile(path, sent); err != nil {
		t.Fatal(err)
	}
	back, err := readJSONFile[gitops.ExtractReport](path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Diff, patch) {
		t.Fatalf("patch did not survive the contract file:\n sent %q\n back %q", patch, back.Diff)
	}
	if back.BaseSHA != sent.BaseSHA || back.TreeHash != sent.TreeHash || !back.Changed {
		t.Fatalf("report = %+v, want %+v", back, sent)
	}
}
