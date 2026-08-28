package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// defaultAuditPageLimit and maxAuditPageLimit bound one page. This read is
// paginated FROM THE OUTSET, and here that is not the usual caution: every
// other list in this system is bounded by something a human made — the runs a
// person started, the projects they were granted — while audit_entries grows
// with every request the server serves. "Unpaginated is fine at month-one
// volume" is already false for this one table.
const (
	defaultAuditPageLimit int32 = 50
	maxAuditPageLimit     int32 = 200
)

// PageEntry is one audit row as READ. It is deliberately not the sqlc row
// (models.go's AuditEntry), which carries detail as raw jsonb bytes: Record
// takes Detail as a map and marshals it, so a reader handing back []byte would
// make this package asymmetric about its own column and every caller would
// repeat the same decode.
type PageEntry struct {
	ID            int64
	OccurredAt    time.Time
	Actor         uuid.NullUUID
	Action        string
	SubjectKind   string
	SubjectID     string
	CorrelationID string
	Detail        map[string]any
}

// Page is one page of entries plus the marker for the next. NextCursor is
// empty on the last page — the HTTP layer renders that as a null, which is a
// value the client branches on rather than a key it fails to find.
type Page struct {
	Entries    []PageEntry
	NextCursor string
}

// Reader is the read half of the recorder. It HOLDS its DBTX, where Record
// takes one per call, and the asymmetry is the honest one: a writer joins the
// caller's transaction because an audit write must commit or roll back with the
// fact it records, while a read has no transaction to join and always goes
// through the pool.
type Reader struct {
	db DBTX
}

func NewReader(db DBTX) *Reader { return &Reader{db: db} }

// encodeAuditCursor is the opaque page marker. It carries BOTH halves of the
// sort key, so the cursor and the ORDER BY cannot disagree about where a page
// ended — the failure that makes a paginated list silently drop or repeat rows.
//
// RFC3339Nano, because occurred_at is stamped from an injected clock and is not
// unique: one transaction emits several entries from a single reading, so
// entries sharing a timestamp are the ordinary case and only the id separates
// them.
func encodeAuditCursor(occurredAt time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(occurredAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(id, 10)))
}

// decodeAuditCursor is deliberately strict. A cursor is opaque to the client,
// so a malformed one is an ordinary thing to receive — from a truncated URL, a
// stale bookmark, a hand-typed value — and every one of them must be a field
// error rather than a panic or a silent page-one.
func decodeAuditCursor(s string) (time.Time, int64, error) {
	invalid := xerrors.E(xerrors.ClassInvalid, "validation failed").
		WithFields(xerrors.FieldError{Field: "cursor", Message: "not a cursor this endpoint issued"})
	// THE ERROR IS LOAD-BEARING, not the bytes. Go's decoder returns what it
	// managed alongside its error, so a padded cursor whose payload is a whole
	// number of quanta hands back a complete and perfectly parseable cursor —
	// and a padded one that is not truncates to a DIFFERENT id that parses just
	// as well. Dropping this error pages from somewhere else, quietly.
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, invalid
	}
	at, rest, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, 0, invalid
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, 0, invalid
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return time.Time{}, 0, invalid
	}
	return occurredAt, id, nil
}

func clampAuditPageLimit(n int32) int32 {
	switch {
	case n <= 0:
		return defaultAuditPageLimit
	case n > maxAuditPageLimit:
		return maxAuditPageLimit
	default:
		return n
	}
}

// ListPage is one page of the global audit log, newest first.
//
// An empty cursor is the first page. A malformed one is a field error, never a
// silent first page: paging is the one place where being quietly wrong looks
// exactly like being right.
func (r *Reader) ListPage(ctx context.Context, cursor string, limit int32) (Page, error) {
	limit = clampAuditPageLimit(limit)
	params := ListAuditPageParams{
		// One extra row is how the next page is DETECTED. Counting is the
		// alternative and it is a second query over the same predicate.
		PageLimit: limit + 1,
	}
	if cursor != "" {
		at, id, err := decodeAuditCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		params.CursorOccurredAt = &at
		params.CursorID = pgtype.Int8{Int64: id, Valid: true}
	}

	rows, err := New(r.db).ListAuditPage(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("audit: list page: %w", err)
	}
	page := Page{Entries: make([]PageEntry, 0, len(rows))}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodeAuditCursor(last.OccurredAt, last.ID)
	}
	for _, row := range rows {
		detail := map[string]any{}
		// detail is NOT NULL with a '{}' default and every writer marshals a
		// map into it, so a row that fails to decode means something wrote to
		// this table outside Record. That is worth failing the read over: an
		// audit log that renders around the row it cannot read is the one log
		// where a silent gap is the whole problem.
		if err := json.Unmarshal(row.Detail, &detail); err != nil {
			return Page{}, fmt.Errorf("audit: decode detail of entry %d: %w", row.ID, err)
		}
		page.Entries = append(page.Entries, PageEntry{
			ID:            row.ID,
			OccurredAt:    row.OccurredAt,
			Actor:         row.ActorUserID,
			Action:        row.Action,
			SubjectKind:   row.SubjectKind,
			SubjectID:     row.SubjectID,
			CorrelationID: row.CorrelationID,
			Detail:        detail,
		})
	}
	return page, nil
}
