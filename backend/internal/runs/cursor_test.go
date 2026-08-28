package runs

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestKeysetCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 123456789, time.UTC)
	id := uuid.MustParse("018f5f0e-0000-7000-8000-000000000001")

	enc := encodeKeysetCursor(at, id)
	gotAt, gotID, err := decodeKeysetCursor(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// NANOSECONDS SURVIVE OR THE CURSOR IS WRONG. uuidv7 ids are minted from a
	// millisecond clock, so two runs created in the same millisecond are
	// separated only by id — and a cursor that truncated the timestamp would
	// re-emit or skip exactly those rows, silently, only under load.
	if !gotAt.Equal(at) {
		t.Errorf("created_at round trip = %v, want %v", gotAt, at)
	}
	if gotID != id {
		t.Errorf("id round trip = %v, want %v", gotID, id)
	}
}

func TestKeysetCursorRejectsGarbage(t *testing.T) {
	// A cursor is OPAQUE to the client, which means a client will eventually
	// send back something that is not one — a truncated URL, a stale bookmark,
	// a hand-typed value. Every one of these must be a 400 with a field error,
	// never a panic and never a silent page-one.
	//
	// EACH INPUT NAMES THE GUARD IT REACHES, and every guard has at least one.
	// This was written with two padded base64 strings ("…MFo=", "…dWlk==") whose
	// comments claimed they tested the shape and the uuid — and RawURLEncoding
	// REFUSES padding, so both were rejected at the decode step and no input in
	// the set ever reached time.Parse or uuid.Parse. Deleting EITHER of those
	// guards outright left every case green (measured, both). A table of
	// malformed values proves nothing about WHICH refusal fired unless the
	// values are chosen to reach one each.
	for _, tc := range []struct{ cursor, guard string }{
		{"", "empty: decodes to nothing, so there is no separator"},
		{"!!!", "not base64 at all"},
		// THE ONE INPUT THAT KILLS THE DECODE GUARD, and it took choosing to
		// make it so. Go's decoder returns the bytes it managed before the
		// error, so a padded string whose payload is a whole number of quanta
		// (57 bytes here: a 20-char RFC3339 stamp, the separator, a 36-char
		// uuid) hands back a COMPLETE, VALID cursor alongside its error.
		// Ignore the error and every later guard waves it through. Any other
		// padded value decodes to a truncated prefix that some later guard
		// refuses anyway, which is what made the first draft of this table
		// unable to kill this guard at all.
		{
			"MjAyNi0wOC0xNlQxMjowMDowMFp8MDE4ZjVmMGUtMDAwMC03MDAwLTgwMDAtMDAwMDAwMDAwMDAx==",
			"padded base64; this endpoint issues unpadded cursors, and the payload is intact behind the padding",
		},
		{"Zm9v", "valid base64 ('foo'), no separator"},
		{"MjAyNi0wOC0xNlQxMjowMDowMFo", "a timestamp and no id: no separator"},
		{"MjAyNi0wOC0xNlQxMjowMDowMFp8bm90LWEtdXVpZA", "separator and a good timestamp, but the id is not a uuid"},
		{"bm90LWEtdGltZXwwMThmNWYwZS0wMDAwLTcwMDAtODAwMC0wMDAwMDAwMDAwMDE", "separator and a good uuid, but the timestamp does not parse"},
		{"bm90LWEtdGltZXxub3QtYS11dWlk", "separator, but neither half parses"},
	} {
		// The separator guard is deliberately not claimed as independently
		// falsifiable: with no "|", the whole string becomes the timestamp half
		// and the empty rest can never parse as a uuid, so removing that guard
		// still leaves the two below to refuse. It stays because it is what
		// makes the failure legible rather than incidental.
		if _, _, err := decodeKeysetCursor(tc.cursor); err == nil {
			t.Errorf("decodeKeysetCursor(%q) accepted garbage — %s", tc.cursor, tc.guard)
		}
	}
}
