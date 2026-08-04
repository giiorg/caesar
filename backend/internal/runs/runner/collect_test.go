package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// unknownUsage is what the log_ref carries when the gateway's bill could not
// be read. It is deliberately not "zero usage": nothing about a missing line
// says the attempt spent nothing.
var unknownUsage = map[string]any{
	"requests": int64(0), "denied": int64(0),
	"request_bytes": int64(0), "response_bytes": int64(0),
	"connects": int64(0), "connect_bytes": int64(0),
	"parse_error": true,
}

func TestEgressUsage_HarvestsTheLastSummaryLine(t *testing.T) {
	summary := func(requests, denied int64) string {
		return `{"time":"2026-08-04T00:00:00Z","level":"INFO","msg":"egress.summary","attempt":"a",` +
			`"requests":` + itoa(requests) + `,"denied":` + itoa(denied) +
			`,"request_bytes":11,"response_bytes":22,"connects":3,"connect_bytes":44}`
	}
	tests := []struct {
		name string
		log  string
		want map[string]any
	}{
		{
			name: "the summary is the last line",
			log: `{"msg":"egress.start","attempt":"a"}` + "\n" +
				`{"msg":"egress.meter","attempt":"a","requests":1,"denied":0}` + "\n" +
				summary(7, 30) + "\n",
			want: map[string]any{
				"requests": int64(7), "denied": int64(30),
				"request_bytes": int64(11), "response_bytes": int64(22),
				"connects": int64(3), "connect_bytes": int64(44),
			},
		},
		{
			name: "a later summary supersedes an earlier one",
			log:  summary(1, 1) + "\n" + summary(7, 30) + "\n",
			want: map[string]any{
				"requests": int64(7), "denied": int64(30),
				"request_bytes": int64(11), "response_bytes": int64(22),
				"connects": int64(3), "connect_bytes": int64(44),
			},
		},
		{
			name: "trailing noise does not hide the summary",
			log:  summary(7, 30) + "\nnot json at all\n{\"msg\":\"egress.stop\"}\n",
			want: map[string]any{
				"requests": int64(7), "denied": int64(30),
				"request_bytes": int64(11), "response_bytes": int64(22),
				"connects": int64(3), "connect_bytes": int64(44),
			},
		},
		{
			name: "a gateway killed before it flushed",
			log:  `{"msg":"egress.start","attempt":"a"}` + "\n",
			want: unknownUsage,
		},
		{
			name: "a summary torn mid-write",
			log:  `{"msg":"egress.summary","attempt":"a","requests":7,"deni`,
			want: unknownUsage,
		},
		{
			name: "an empty log",
			log:  "",
			want: unknownUsage,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), gatewayLogFile)
			if err := os.WriteFile(path, []byte(tc.log), 0o600); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, egressUsage(path)); diff != "" {
				t.Errorf("egress usage (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEgressUsage_AMissingGatewayLogIsUnknownUsage(t *testing.T) {
	got := egressUsage(filepath.Join(t.TempDir(), "absent.log"))
	if diff := cmp.Diff(unknownUsage, got); diff != "" {
		t.Errorf("egress usage (-want +got):\n%s", diff)
	}
}

func TestEgressUsage_ReadsTheSummaryPastAHugePrefix(t *testing.T) {
	// The harvest reads a tail, not the whole file: a chatty gateway must
	// not push its own bill out of reach.
	path := filepath.Join(t.TempDir(), gatewayLogFile)
	noise := strings.Repeat(`{"msg":"egress.connect","host":"a.example"}`+"\n", 20_000)
	line := `{"msg":"egress.summary","requests":2,"denied":0,"request_bytes":0,"response_bytes":0,"connects":1,"connect_bytes":9}`
	if err := os.WriteFile(path, []byte(noise+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := egressUsage(path)["connect_bytes"]; got != int64(9) {
		t.Errorf("connect_bytes = %v, want 9", got)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
