//go:build integration

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
)

// seedT0 is later than newTestServer's fake clock (2026-08-02T12:00:00Z), so
// seeded entries sort ahead of the ones bootstrap and login write for
// themselves. That is not decoration: this endpoint is newest-first, and a
// fixture stamped at or before identity's own rows would interleave with them
// and make every ordering assertion below depend on tie-breaking it never
// meant to test.
var seedT0 = time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)

// seedAudit appends one entry at an exact instant. It drives Recorder rather
// than INSERTing, so the rows under test are written by the same code paths
// production uses — including the detail marshalling the reader has to undo.
func seedAudit(t *testing.T, pool *pgxpool.Pool, at time.Time, action, subjectID string) {
	t.Helper()
	rec := audit.NewRecorder(clock.NewFake(at))
	err := rec.Record(context.Background(), pool, audit.Entry{
		Action: action, SubjectKind: "test", SubjectID: subjectID,
		CorrelationID: "corr-" + subjectID,
		Detail:        map[string]any{"seq": subjectID},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// clearAudit empties the table so a test can count exactly.
//
// IT HAS TO EXIST. This endpoint is admin-only, and every way to obtain an
// admin writes audit rows — Bootstrap records one and Login records another,
// measured at 2 rows for bootstrapAndLogin — so "an empty audit table behind an
// authenticated admin" is a state the HTTP surface cannot reach on its own.
// Truncating here is a fixture move and not a claim about the table, which
// stays append-only in production.
func clearAudit(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "DELETE FROM audit_entries"); err != nil {
		t.Fatal(err)
	}
}

func auditPage(t *testing.T, srv, cookie, query string) identityapi.AuditPageResponse {
	t.Helper()
	url := srv + "/api/v1/audit"
	if query != "" {
		url += "?" + query
	}
	resp := do(t, http.MethodGet, url, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	return decodeInto[identityapi.AuditPageResponse](t, resp)
}

func TestAudit_MemberIsRefused(t *testing.T) {
	srv, svc, _ := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)

	resp := do(t, http.MethodPost, srv.URL+"/api/v1/users", admin, identityapi.CreateUserRequest{
		Email: "m@example.com", Password: testPassword, Role: "member",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create member = %d", resp.StatusCode)
	}
	resp = do(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "",
		identityapi.LoginRequest{Email: "m@example.com", Password: testPassword})
	memberTok := sessionCookie(t, resp)

	// 403 and not an empty 200. The audit log is GLOBAL — there is no grant
	// that scopes a subset of it — so unlike the run list, which answers an
	// ungranted member with an empty page, this endpoint has nothing honest to
	// return but a refusal.
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/audit", memberTok, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member GET /audit = %d, want 403", resp.StatusCode)
	}
	// And an anonymous caller is refused before the role is ever consulted.
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/audit", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /audit = %d, want 401", resp.StatusCode)
	}
}

func TestAudit_NewestFirst(t *testing.T) {
	srv, svc, pool := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)
	clearAudit(t, pool)

	seedAudit(t, pool, seedT0, "test.one", "1")
	seedAudit(t, pool, seedT0.Add(time.Minute), "test.two", "2")
	seedAudit(t, pool, seedT0.Add(2*time.Minute), "test.three", "3")

	page := auditPage(t, srv.URL, admin, "")
	if len(page.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(page.Entries))
	}
	for i, want := range []string{"3", "2", "1"} {
		if page.Entries[i].SubjectID != want {
			t.Errorf("entry %d subject = %q, want %q (newest first)", i, page.Entries[i].SubjectID, want)
		}
	}
	if page.NextCursor != nil {
		t.Errorf("next_cursor = %q on a complete page, want null", *page.NextCursor)
	}

	// The projection carries the whole row, and detail survives the jsonb round
	// trip as an object rather than a string.
	newest := page.Entries[0]
	if newest.Action != "test.three" || newest.SubjectKind != "test" || newest.CorrelationID != "corr-3" {
		t.Errorf("newest entry = %+v", newest)
	}
	if newest.Detail["seq"] != "3" {
		t.Errorf("detail = %v, want the seeded map", newest.Detail)
	}
	if !newest.OccurredAt.Equal(seedT0.Add(2 * time.Minute)) {
		t.Errorf("occurred_at = %v, want the seeding clock's instant", newest.OccurredAt)
	}
	// A seeded entry has no actor, and the contract says that is null rather
	// than an absent key.
	if newest.ActorUserID != nil {
		t.Errorf("actor_user_id = %v on a system entry, want null", *newest.ActorUserID)
	}
}

func TestAudit_PagesAcrossASharedTimestamp(t *testing.T) {
	srv, svc, pool := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)
	clearAudit(t, pool)

	// THE MIDDLE TWO SHARE occurred_at EXACTLY, and the page boundary is drawn
	// between them. This is the fixture the composite cursor exists for: with a
	// timestamp-only cursor, `occurred_at < $1` skips the twin and
	// `occurred_at <= $1` repeats it, and both failures are invisible on any
	// fixture whose timestamps are distinct.
	seedAudit(t, pool, seedT0, "test.oldest", "1")
	seedAudit(t, pool, seedT0.Add(time.Minute), "test.twin", "2")
	seedAudit(t, pool, seedT0.Add(time.Minute), "test.twin", "3")
	seedAudit(t, pool, seedT0.Add(2*time.Minute), "test.newest", "4")

	var got []string
	query := "limit=2"
	for pages := 0; ; pages++ {
		if pages > 4 {
			t.Fatalf("did not terminate after %d pages; collected %v", pages, got)
		}
		page := auditPage(t, srv.URL, admin, query)
		for _, e := range page.Entries {
			got = append(got, e.SubjectID)
		}
		if page.NextCursor == nil {
			break
		}
		if len(page.Entries) != 2 {
			t.Fatalf("a page with a next_cursor held %d entries, want the full 2", len(page.Entries))
		}
		query = "limit=2&cursor=" + *page.NextCursor
	}

	// EXACTLY ONCE AND IN ORDER — ids 3 then 2 within the shared instant,
	// because (occurred_at DESC, id DESC) breaks the tie by insertion order.
	want := []string{"4", "3", "2", "1"}
	if len(got) != len(want) {
		t.Fatalf("collected %v across pages, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collected %v, want %v", got, want)
		}
	}
}

func TestAudit_ActorIsPresentWhenThereIsOne(t *testing.T) {
	srv, svc, _ := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)

	// NOT SEEDED — these are the rows bootstrap and login wrote for
	// themselves, and they are here because they carry the two halves of the
	// nullable actor between them. Every seeded fixture above is actorless, so
	// on those alone a projection that dropped the actor entirely would pass.
	// Measured: bootstrapAndLogin leaves exactly these 2 rows.
	page := auditPage(t, srv.URL, admin, "")
	if len(page.Entries) != 2 {
		t.Fatalf("entries = %d, want the 2 rows bootstrap and login write", len(page.Entries))
	}
	login, bootstrap := page.Entries[0], page.Entries[1]
	if login.Action != "auth.login" || bootstrap.Action != "user.bootstrapped" {
		t.Fatalf("actions = %q, %q — want auth.login then user.bootstrapped", login.Action, bootstrap.Action)
	}
	// A human did this one, so the actor is a uuid.
	if login.ActorUserID == nil {
		t.Fatal("actor_user_id = null on auth.login, want the user who logged in")
	}
	me := decodeInto[identityapi.MeResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", admin, nil))
	if *login.ActorUserID != me.User.ID {
		t.Errorf("actor_user_id = %q, want the logged-in user %q", *login.ActorUserID, me.User.ID)
	}
	// Nobody did this one — bootstrap runs before there is anyone to do it —
	// and null is the honest answer rather than a zero uuid.
	if bootstrap.ActorUserID != nil {
		t.Errorf("actor_user_id = %q on user.bootstrapped, want null", *bootstrap.ActorUserID)
	}
}

func TestAudit_EmptyTableIsAnEmptyPage(t *testing.T) {
	srv, svc, pool := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)
	clearAudit(t, pool)

	page := auditPage(t, srv.URL, admin, "")
	// An EMPTY ARRAY, not null. The DTO makes entries required, and a client
	// that has to branch on null before it can iterate is a client the contract
	// failed.
	if page.Entries == nil {
		t.Fatal("entries = null on an empty page, want []")
	}
	if len(page.Entries) != 0 {
		t.Fatalf("entries = %d on an empty table, want 0", len(page.Entries))
	}
	if page.NextCursor != nil {
		t.Errorf("next_cursor = %q on an empty page, want null", *page.NextCursor)
	}
}

func TestAudit_CursorAtTheExactBoundaryEndsThePaging(t *testing.T) {
	srv, svc, pool := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)
	clearAudit(t, pool)

	seedAudit(t, pool, seedT0, "test.one", "1")
	seedAudit(t, pool, seedT0.Add(time.Minute), "test.two", "2")

	// limit EQUALS the number of remaining rows. This is the case the limit+1
	// probe exists for: a reader that decided "a full page means there is more"
	// issues a cursor here, and the client makes one extra request that returns
	// nothing — which looks like working software right up until a screen
	// renders an empty second page.
	first := auditPage(t, srv.URL, admin, "limit=2")
	if len(first.Entries) != 2 {
		t.Fatalf("first page = %d entries, want 2", len(first.Entries))
	}
	if first.NextCursor != nil {
		t.Fatalf("next_cursor = %q when the page consumed every row, want null", *first.NextCursor)
	}

	// And the cursor issued by a genuinely-truncated page leads to a terminal
	// page rather than to another cursor.
	truncated := auditPage(t, srv.URL, admin, "limit=1")
	if truncated.NextCursor == nil {
		t.Fatal("next_cursor = null with a row still to come, want a cursor")
	}
	last := auditPage(t, srv.URL, admin, "limit=1&cursor="+*truncated.NextCursor)
	if len(last.Entries) != 1 || last.Entries[0].SubjectID != "1" {
		t.Fatalf("second page = %+v, want the single older entry", last.Entries)
	}
	if last.NextCursor != nil {
		t.Errorf("next_cursor = %q on the last page, want null", *last.NextCursor)
	}
}

func TestAudit_RejectsAMalformedCursorAndLimit(t *testing.T) {
	srv, svc, _ := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)

	// A cursor is opaque, so a client WILL eventually send back something that
	// is not one. A 400 naming the field is the only answer that lets it
	// recover; a silent first page is how a list quietly restarts forever.
	for _, q := range []string{"cursor=not-a-cursor", "cursor=" + "MjAyNi0wOC0xNlQxMjowMDowMFp8MTIz=="} {
		resp := do(t, http.MethodGet, srv.URL+"/api/v1/audit?"+q, admin, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /audit?%s = %d, want 400", q, resp.StatusCode)
		}
	}
	for _, q := range []string{"limit=0", "limit=-3", "limit=abc"} {
		resp := do(t, http.MethodGet, srv.URL+"/api/v1/audit?"+q, admin, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /audit?%s = %d, want 400", q, resp.StatusCode)
		}
	}
	// An oversized limit is CLAMPED, not refused: the cap is this server's
	// protection and a page of 200 is a complete answer to "give me 100000".
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/audit?limit=100000", admin, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /audit?limit=100000 = %d, want 200 (clamped)", resp.StatusCode)
	}
}
