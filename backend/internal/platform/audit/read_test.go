package audit

import (
	"testing"
	"time"
)

func TestAuditCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 123456789, time.UTC)
	const id int64 = 918273645

	enc := encodeAuditCursor(at, id)
	gotAt, gotID, err := decodeAuditCursor(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// NANOSECONDS SURVIVE OR THE CURSOR IS WRONG. occurred_at comes off the
	// injected clock and is not unique — one transaction stamps every entry it
	// emits from a single reading — so rows sharing a timestamp are the normal
	// case, not the edge one, and a truncated timestamp would re-emit or skip
	// exactly those rows, silently.
	if !gotAt.Equal(at) {
		t.Errorf("occurred_at round trip = %v, want %v", gotAt, at)
	}
	if gotID != id {
		t.Errorf("id round trip = %d, want %d", gotID, id)
	}
}

func TestAuditCursorRejectsGarbage(t *testing.T) {
	// A cursor is OPAQUE to the client, so a client will eventually send back
	// something that is not one — a truncated URL, a stale bookmark, a
	// hand-typed value. Every one must be a field error, never a panic and
	// never a silent page-one.
	//
	// EACH INPUT NAMES THE GUARD IT REACHES. The run-list cursor's table
	// (runs/runlist_test.go) records why that matters: a table of merely
	// malformed values proves nothing about WHICH refusal fired, and its first
	// draft could not kill the base64 guard at all.
	for _, tc := range []struct{ cursor, guard string }{
		{"", "empty: decodes to nothing, so there is no separator"},
		{"!!!", "not base64 at all"},
		// THE INPUT THAT KILLS THE DECODE GUARD, and the id in it was chosen
		// arithmetically. Go's decoder hands back the bytes it managed
		// alongside its error, so a padded string whose payload is a whole
		// number of 3-byte quanta decodes to a COMPLETE, VALID cursor: 20
		// characters of RFC3339, the separator, and a 3-digit id is exactly 24.
		// Ignore the error and every later guard waves it through.
		{
			"MjAyNi0wOC0xNlQxMjowMDowMFp8MTIz==",
			"padded base64; this endpoint issues unpadded cursors, and a 3-digit id leaves the payload intact behind the padding",
		},
		// The same guard, failing the quieter way. This one's payload is 25
		// bytes, so the padded decode truncates `4567` to `456` — a cursor that
		// parses perfectly and points at the WRONG ROW. A decoder that ignored
		// the error would not error here; it would page from somewhere else.
		{
			"MjAyNi0wOC0xNlQxMjowMDowMFp8NDU2Nw==",
			"padded base64 whose truncation is silent: it still parses, at a different id",
		},
		{"Zm9v", "valid base64 ('foo'), no separator"},
		{"MjAyNi0wOC0xNlQxMjowMDowMFo", "a timestamp and no id: no separator"},
		{"MjAyNi0wOC0xNlQxMjowMDowMFp8bm90LWFuLWlk", "separator and a good timestamp, but the id is not an integer"},
		{"MjAyNi0wOC0xNlQxMjowMDowMFp8MTIuNQ", "separator and a good timestamp, but the id is not an INTEGER — 12.5"},
		{"bm90LWEtdGltZXwxMjM", "separator and a good id, but the timestamp does not parse"},
		{"bm90LWEtdGltZXxub3QtYW4taWQ", "separator, but neither half parses"},
	} {
		// The separator guard is deliberately not claimed as independently
		// falsifiable: with no "|", the whole string becomes the timestamp half
		// and the empty rest can never parse as an integer, so removing it
		// still leaves the two below to refuse. It stays because it makes the
		// failure legible rather than incidental.
		if _, _, err := decodeAuditCursor(tc.cursor); err == nil {
			t.Errorf("decodeAuditCursor(%q) accepted garbage — %s", tc.cursor, tc.guard)
		}
	}
}

func TestAuditPageLimitClamp(t *testing.T) {
	// The cap is this server's protection and not a contract the caller agreed
	// to, so an oversized limit is CLAMPED rather than refused: a page of 200
	// is a complete answer to "give me 1000". Zero means the caller passed no
	// limit at all, which is the default and not an empty page.
	for _, tc := range []struct {
		in, want int32
		why      string
	}{
		{0, defaultAuditPageLimit, "absent: the query param was not sent"},
		{-1, defaultAuditPageLimit, "negative: nothing else is a sane reading"},
		{1, 1, "the smallest real page survives untouched"},
		{50, 50, "the default asked for explicitly is still the default"},
		{maxAuditPageLimit, maxAuditPageLimit, "the cap itself is not over the cap"},
		{maxAuditPageLimit + 1, maxAuditPageLimit, "one over clamps"},
		{100000, maxAuditPageLimit, "far over clamps to the same place"},
	} {
		if got := clampAuditPageLimit(tc.in); got != tc.want {
			t.Errorf("clampAuditPageLimit(%d) = %d, want %d — %s", tc.in, got, tc.want, tc.why)
		}
	}
}
