package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

func TestReadJSONFile_ContractDecodingIsStrict(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		want    gitops.StageInput
	}{
		{
			name: "accepts the contract fields",
			body: `{"clone_url":"https://example.com/a/b.git","base_sha":"deadbeef","quota_bytes":10,"quota_files":2}`,
			want: gitops.StageInput{CloneURL: "https://example.com/a/b.git", BaseSHA: "deadbeef", QuotaBytes: 10, QuotaFiles: 2},
		},
		{
			// The lineage half of the contract is what the runner writes and
			// this decoder is the only thing that reads it: a json tag that
			// drifted would fail the whole file here rather than quietly hand
			// a phase an empty workspace.
			name: "accepts the lineage fields",
			body: `{"clone_url":"u","base_sha":"s","instructions":"do it",` +
				`"task_inputs":{"ticket":"CAESAR-12"},"patch":"ZGlmZgo=",` +
				`"contexts":[{"name":"0-plan.json","json":{"content":"the plan"}}],` +
				`"input_refs":[{"id":"a","type":"patch"},{"id":"b","type":"plan","name":"0-plan.json"}]}`,
			want: gitops.StageInput{
				CloneURL: "u", BaseSHA: "s", Instructions: "do it",
				TaskInputs: map[string]any{"ticket": "CAESAR-12"},
				Patch:      "ZGlmZgo=",
				Contexts:   []gitops.StageContext{{Name: "0-plan.json", JSON: json.RawMessage(`{"content":"the plan"}`)}},
				InputRefs: []gitops.InputRef{
					{ID: "a", Type: "patch"},
					{ID: "b", Type: "plan", Name: "0-plan.json"},
				},
			},
		},
		{name: "rejects an unknown field", body: `{"clone_url":"x","token":"sekret"}`, wantErr: true},
		{name: "rejects malformed json", body: `{`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stage.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readJSONFile[gitops.StageInput](path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decoded %s without error: %+v", tc.body, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Fatalf("decoded contract mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWriteJSONFile_ReportRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stage.report.json")
	want := gitops.StageReport{HeadSHA: "abc", Files: 3, Bytes: 12, Verified: true}
	if err := writeJSONFile(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readJSONFile[gitops.StageReport](path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
