package runs

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// The keyset cursor this context's paginated reads mint. It lives in its own
// file rather than inside either caller, because it is now the wire format of
// two endpoints — GET /runs and GET /approvals/decisions — and a second copy
// of it would drift silently: each copy round-trips itself perfectly, so
// nothing fails until a cursor crosses from one to the other.
//
// Both callers page over (timestamptz, uuid), which is why one function serves
// them. A read keyed on a different id type gets its own codec rather than a
// generic one — platform/audit's is over a bigint identity and is deliberately
// separate.

// encodeKeysetCursor is the opaque page marker. It carries BOTH halves of the
// sort key, so the cursor and the ORDER BY cannot disagree about where a page
// ended — the failure that makes a paginated list silently drop or repeat rows.
//
// RFC3339Nano, because uuidv7 ids come off a millisecond clock: two rows
// stamped in the same millisecond are separated only by id, and a truncated
// timestamp would put them on the wrong side of the boundary.
func encodeKeysetCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

// decodeKeysetCursor is deliberately strict. A cursor is opaque to the client,
// so a malformed one is an ordinary thing to receive — from a truncated URL, a
// stale bookmark, a hand-typed value — and every one of them must be a field
// error rather than a panic or a silent page-one.
func decodeKeysetCursor(s string) (time.Time, uuid.UUID, error) {
	invalid := xerrors.E(xerrors.ClassInvalid, "validation failed").
		WithFields(xerrors.FieldError{Field: "cursor", Message: "not a cursor this endpoint issued"})
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, invalid
	}
	at, rest, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, invalid
	}
	stamp, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, uuid.Nil, invalid
	}
	id, err := uuid.Parse(rest)
	if err != nil {
		return time.Time{}, uuid.Nil, invalid
	}
	return stamp, id, nil
}
