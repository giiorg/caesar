package workflows

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The seeded editor corpus, judged by the real validator.
//
// WHAT THIS TEST IS FOR (spec §6.3). The web editor's acceptance bar is
// "random editor operation sequences derive documents that parse and validate
// SERVER-side". Its subject is TypeScript — the reducer in
// web/src/workflows/editor-state.ts and the derivation in derive.ts — which no
// Go generator can exercise; its verdict is Validate below, which no TypeScript
// test can call. The two meet as BYTES: web/tools/gen-editor-corpus.mts folds
// generated operation sequences through the production reducer, writes the
// documents to testdata/editor_corpus/, and `contracts:gen` regenerates them so
// `contracts:check` fails on any drift. Change a derivation rule and either the
// corpus diffs or the judge below refuses the new documents. Red either way.
//
// NO BUILD TAG. It has to run in plain `backend:test`, because the whole point
// is that a TypeScript change reddens the cheapest lane there is.
const corpusDir = "testdata/editor_corpus"

// manifestFile is the EXACT expected set. Globbing alone accepts an orphan left
// behind by a removed seed — still valid JSON, validated forever, produced by
// no rule the editor still has — which is precisely the failure the generator's
// reconciler and this file's count assertion exist as a pair to catch.
type corpusManifest struct {
	Files map[string]string `json:"files"` // name -> sha256 of the file's bytes
}

func TestEditorCorpusValidates(t *testing.T) {
	man := readManifest(t)
	if len(man.Files) == 0 {
		t.Fatal("editor corpus manifest is empty")
	}

	files, err := filepath.Glob(filepath.Join(corpusDir, "seed-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(man.Files) {
		t.Fatalf("corpus has %d files, manifest declares %d — an orphan or a missing document",
			len(files), len(man.Files))
	}

	// Distinct DOCUMENTS and distinct STEP SEQUENCES, tracked separately —
	// see the diversity assertions at the bottom for why one of them is not
	// enough.
	shapes := map[string]bool{}
	sequences := map[string]bool{}
	seenTypes := map[NodeType]bool{}

	for _, f := range files {
		name := filepath.Base(f)
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != man.Files[name] {
			t.Errorf("%s: sha256 %s, manifest says %s — regenerate the corpus", name, got, man.Files[name])
			continue
		}

		doc, err := ParseDocument(raw)
		if err != nil {
			t.Errorf("%s: parse: %v", name, err)
			continue
		}
		if v := Validate(doc); len(v) != 0 {
			t.Errorf("%s: %d violations, first: %s %s", name, len(v), v[0].Path, v[0].Message)
		}

		// THE EDITOR EMITS CANONICAL BYTES, and this is where that stops being
		// an aspiration. Canonical() is what CreateVersion hashes to decide
		// whether a save registers a new version or replays an existing one
		// (workflows.go), so a TypeScript emit that agreed in MEANING but not
		// in key order would mint a second version of an unchanged workflow —
		// silently, and only in production. The corpus is generated with
		// JSON.stringify over objects built in this struct's field order;
		// this comparison is what holds those two orders together.
		if canonical := doc.Canonical(); string(canonical) != string(raw) {
			t.Errorf("%s: bytes are not canonical\n on disk: %s\ncanonical: %s", name, raw, canonical)
		}

		shapes[doc.Hash()] = true
		var seq []string
		for _, n := range doc.Nodes {
			seq = append(seq, string(n.Type))
			seenTypes[n.Type] = true
		}
		sequences[strings.Join(seq, "·")] = true
	}

	// DIVERSITY, stated as numbers so a generator that degenerates is RED.
	// Twenty-four seeds that all fold to the same minimal document satisfy
	// every assertion above while exercising one code path.
	//
	// TWO FLOORS, because the obvious one is weak on its own: distinct hashes
	// count documents that differ in ANY byte, so a generator that emitted the
	// same three-node shape with different instructions every time would clear
	// it while covering nothing. The step SEQUENCE is what the derivation
	// actually branches on — the optional plan, the number of implements, the
	// optional review — so its floor is the one that would catch that.
	if len(shapes) < 8 {
		t.Errorf("corpus collapsed to %d distinct documents across %d files", len(shapes), len(files))
	}
	if len(sequences) < 6 {
		t.Errorf("corpus covers only %d distinct step sequences: %v", len(sequences), keys(sequences))
	}

	// EVERY NODE TYPE APPEARS SOMEWHERE, iterating definition.go's OWN
	// vocabulary rather than a list restated here — which is the only way "a
	// new node type reddens the corpus" is true rather than hoped for. Adding
	// one to NodeTypes() fails this until the editor's grammar can place it and
	// a seed produces it.
	for _, nt := range NodeTypes() {
		if !seenTypes[nt] {
			t.Errorf("no corpus document contains a %q node", nt)
		}
	}
}

// A hand-written file that was never generated is the failure a diff gate
// CANNOT see: `contracts:check` runs `contracts:gen` first, so a deleted
// artifact is restored byte-for-byte and the check goes green. The orphan is
// the other direction, and the manifest is what makes it visible.
func readManifest(t *testing.T) corpusManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDir, "manifest.json"))
	if err != nil {
		t.Fatalf("editor corpus manifest missing: %v", err)
	}
	var man corpusManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("editor corpus manifest: %v", err)
	}
	return man
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
