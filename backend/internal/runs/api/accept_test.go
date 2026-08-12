package api

import "testing"

func TestAcceptsEventStream(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		header string
		want   bool
	}{
		// The existing poll's contract: absent, wildcard and unrelated Accept
		// headers all keep the JSON branch. A client that never heard of this
		// plan must not be handed a stream.
		{"absent", "", false},
		{"wildcard", "*/*", false},
		{"json", "application/json", false},
		{"a type wildcard is not specific enough", "text/*", false},
		{"browser EventSource", "text/event-stream", true},
		{"with parameters", "text/event-stream; charset=utf-8", true},
		{"case-insensitive", "TEXT/EVENT-STREAM", true},
		// The whole reason this is a parser and not strings.Contains.
		{"explicitly refused", "application/json, text/event-stream;q=0", false},
		{"explicitly refused, decimal", "application/json, text/event-stream;q=0.0", false},
		{"weighted but wanted", "application/json;q=0.1, text/event-stream;q=0.9", true},
		{"tiny weight still counts", "text/event-stream;q=0.001", true},
		{"unparseable weight is a refusal", "text/event-stream;q=later", false},
		{"an unparseable range is skipped, not fatal", "🙂, text/event-stream", true},
		{"a refusal elsewhere does not refuse this", "text/event-stream, application/json;q=0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := acceptsEventStream(tc.header); got != tc.want {
				t.Errorf("acceptsEventStream(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}
