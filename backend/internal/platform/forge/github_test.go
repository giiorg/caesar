package forge_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/platform/forge"
)

func TestGitHub_HeadResolvesTheDefaultBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/acme/widgets/commits/main"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		_, _ = io.WriteString(w, `{"sha":"deadbeef"}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	sha, err := c.Head(context.Background(), forge.Repo{Slug: "acme/widgets", DefaultBranch: "main"}, forge.Auth{Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if sha != "deadbeef" {
		t.Fatalf("Head = %q, want deadbeef", sha)
	}
}

func TestGitHub_BranchHeadReadsTheRefObjectSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath, not Path: the server decodes %2F back into Path, so Path
		// is identical whether or not the client escaped the branch — asserting
		// on it would prove nothing. GitHub's git/ref/heads/{ref} treats
		// everything after heads/ as ONE segment, so caesar/abc has to arrive as
		// caesar%2Fabc or it addresses a different ref.
		if got, want := r.URL.EscapedPath(), "/repos/acme/widgets/git/ref/heads/caesar%2Fabc"; got != want {
			t.Errorf("escaped path = %q, want %q", got, want)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("api version header = %q; pinning it is what stops a silent shape change", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "caesar" {
			t.Errorf("User-Agent = %q; a forge that rate-limits by agent has to be able to name us", got)
		}
		_, _ = io.WriteString(w, `{"ref":"refs/heads/caesar/abc","object":{"sha":"deadbeef","type":"commit"}}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	sha, ok, err := c.BranchHead(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "tok"}, "caesar/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || sha != "deadbeef" {
		t.Fatalf("BranchHead = %q,%v; want deadbeef,true", sha, ok)
	}
}

func TestGitHub_BranchHeadReportsAbsenceRatherThanFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	sha, ok, err := c.BranchHead(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "tok"}, "nope")
	if err != nil {
		t.Fatalf("an absent branch is an answer, not an error: %v", err)
	}
	if ok || sha != "" {
		t.Fatalf("BranchHead = %q,%v; want \"\",false", sha, ok)
	}
}

func TestGitHub_CreatePullRequestReturnsItsReceipt(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/widgets/pulls" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"number":42,"html_url":"https://github.com/acme/widgets/pull/42","state":"open","head":{"sha":"deadbeef"},"base":{"ref":"main"}}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	got, err := c.CreatePullRequest(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "tok"},
		forge.PullRequest{Head: "caesar/abc", Base: "main", Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	// Base is part of the receipt, not decoration: recovery locates a request
	// by head branch alone, and a candidate with no base is unprovable.
	want := forge.PullRequestRef{Number: 42, URL: "https://github.com/acme/widgets/pull/42", State: "open", HeadSHA: "deadbeef", Base: "main"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if body["head"] != "caesar/abc" || body["base"] != "main" {
		t.Errorf("request body = %v", body)
	}
}

func TestGitHub_ErrorsAreClassifiedForRetry(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		headers      map[string]string
		body         string
		wantRetry    bool
		wantConflict bool
		// wantAt is set where the forge states an ABSOLUTE reset, which can be
		// compared to a constant.
		wantAt time.Time
		// wantDelta is set where the forge states a RELATIVE one. That is still
		// exact rather than approximate: the client reads its clock strictly
		// between this test's `before` and `after`, so the instant it computes
		// lies in [before+delta, after+delta] by construction, and all three
		// carry monotonic readings, so no wall-clock jump can widen the window.
		wantDelta time.Duration
	}{
		{name: "pr exists", status: http.StatusUnprocessableEntity, wantConflict: true},
		// 403 is NOT a synonym for "permanently forbidden" on GitHub: both
		// primary and secondary rate limits are documented to answer 403 or
		// 429. Treating every 403 as permanent turns a few minutes of
		// throttling into a failed run with a branch already on the forge —
		// and re-requesting immediately makes a secondary limit worse.
		{name: "forbidden, no rate signal", status: http.StatusForbidden, wantRetry: false},
		{
			name: "primary limit as 403", status: http.StatusForbidden, wantRetry: true,
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1893456000"},
			wantAt:  time.Unix(1893456000, 0).UTC(),
		},
		{
			name: "secondary limit as 403", status: http.StatusForbidden, wantRetry: true,
			headers:   map[string]string{"Retry-After": "60"},
			body:      `{"message":"You have exceeded a secondary rate limit"}`,
			wantDelta: 60 * time.Second,
		},
		{name: "too many requests", status: http.StatusTooManyRequests, wantRetry: true},
		{name: "bad gateway", status: http.StatusBadGateway, wantRetry: true},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantRetry: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
				body := c.body
				if body == "" {
					body = `{"message":"nope"}`
				}
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()

			cl := forge.NewGitHub(srv.URL, srv.Client())
			// The bracket is what lets a relative reset be asserted exactly: the
			// client's own time.Now() falls strictly inside it.
			before := time.Now()
			_, err := cl.CreatePullRequest(context.Background(), forge.Repo{Slug: "a/b"}, forge.Auth{Token: "t"}, forge.PullRequest{})
			after := time.Now()
			if err == nil {
				t.Fatalf("status %d must error", c.status)
			}
			if got := forge.IsRetryable(err); got != c.wantRetry {
				t.Errorf("retryable = %v, want %v", got, c.wantRetry)
			}
			if got := errors.Is(err, forge.ErrConflict); got != c.wantConflict {
				t.Errorf("conflict = %v, want %v", got, c.wantConflict)
			}
			// A stated reset must survive as an instant, not just as "retryable":
			// it is the difference between backing off blind and waiting exactly
			// as long as the forge asked.
			if at, ok := forge.RetryAfter(err); c.wantRetry && len(c.headers) > 0 && !ok {
				t.Errorf("rate-limited error must carry a retry instant (got %v)", at)
			}
			// And it must be the RIGHT instant. Presence alone would accept an
			// epoch read as milliseconds — 1893456000 becomes the year 61989, and
			// a run whose RetryAt is a geological age away parks until its wall
			// deadline instead of retrying in minutes.
			if !c.wantAt.IsZero() {
				at, ok := forge.RetryAfter(err)
				if !ok || !at.Equal(c.wantAt) {
					t.Errorf("retry instant = %v (present=%v), want %v", at, ok, c.wantAt)
				}
			}
			// A stated DELAY has to be added to the clock, not swallowed. Coming
			// back at `now` retries into a window the forge just said is shut,
			// and on a secondary limit re-requesting immediately is what makes
			// the limit worse — the argument the table's own comment opens with.
			if c.wantDelta > 0 {
				at, ok := forge.RetryAfter(err)
				if !ok || at.Before(before.Add(c.wantDelta)) || at.After(after.Add(c.wantDelta)) {
					t.Errorf("retry instant = %v (present=%v), want now+%v, i.e. within [%v, %v]",
						at, ok, c.wantDelta, before.Add(c.wantDelta), after.Add(c.wantDelta))
				}
			}
		})
	}
}

func TestGitHub_ErrorsNeverEchoTheToken(t *testing.T) {
	// The error is written into a run event and an intent's halt_reason. A
	// forge that reflected the Authorization header back would put a live
	// credential in the database (spec §6 redaction).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"bad token `+r.Header.Get("Authorization")+`"}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	_, err := c.CreatePullRequest(context.Background(), forge.Repo{Slug: "a/b"}, forge.Auth{Token: "s3cret"}, forge.PullRequest{})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("the error echoed the token: %q", err)
	}
}

func TestGitHub_FindPullRequestsByHeadIsTheIdempotencyProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("head"); got != "acme:caesar/abc" {
			t.Errorf("head filter = %q; GitHub needs owner-qualified", got)
		}
		if got := r.URL.Query().Get("state"); got != "all" {
			t.Errorf("state = %q; a merged or closed PR is still OUR receipt", got)
		}
		_, _ = io.WriteString(w, `[{"number":42,"html_url":"u","state":"open","head":{"sha":"deadbeef"},"base":{"ref":"main"}}]`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	got, err := c.FindPullRequests(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "t"}, "caesar/abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1: the probe returns EVERY request on the head branch", len(got))
	}
	if got[0].Number != 42 || got[0].HeadSHA != "deadbeef" {
		t.Errorf("receipt = %+v", got[0])
	}
	// Base is what makes a candidate provable downstream: matchPublication
	// treats an empty one as unprovable and halts the publication.
	if got[0].Base != "main" {
		t.Errorf("base = %q, want main", got[0].Base)
	}
}

func TestGitHub_FindPullRequestsFollowsEveryPage(t *testing.T) {
	// Exhaustion is not decoration: recovery adopts a publication only if it
	// can see the candidate, and GitHub caps a page at 100. A probe that read
	// page one would report "no such pull request" for a marker branch with a
	// long history and open a SECOND request for an approval already spent.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("Link", `<`+srv.URL+`/repos/acme/widgets/pulls?page=2>; rel="next"`)
			_, _ = io.WriteString(w, `[{"number":1,"state":"closed","head":{"sha":"a"},"base":{"ref":"main"}}]`)
		case "2":
			_, _ = io.WriteString(w, `[{"number":2,"state":"open","head":{"sha":"b"},"base":{"ref":"main"}}]`)
		default:
			t.Errorf("page %q requested after the last one; the loop is not terminating on the Link header", r.URL.Query().Get("page"))
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	got, err := c.FindPullRequests(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "t"}, "caesar/abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 2 {
		t.Fatalf("candidates = %+v, want both pages in order", got)
	}
}

func TestGitHub_FindPullRequestsRefusesRatherThanTruncateAtTheCap(t *testing.T) {
	// A server that advertises a next page forever must not spin a worker — but
	// stopping quietly at the cap would be worse than spinning. A partial
	// listing reported as complete says "no such pull request" for a branch that
	// has one, and recovery answers that by opening a SECOND request against a
	// single-use approval. So the cap is an error, never a truncated answer, and
	// this asserts the error rather than the length: a truncating implementation
	// returns a plausible-looking slice and must not pass by coincidence.
	var requests atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("Link", "<"+srv.URL+"/repos/acme/widgets/pulls?page="+strconv.Itoa(page+1)+`>; rel="next"`)
		_, _ = io.WriteString(w, `[{"number":1,"state":"open","head":{"sha":"a"},"base":{"ref":"main"}}]`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	if _, err := c.FindPullRequests(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "t"}, "caesar/abc"); err == nil {
		t.Fatal("a listing that never ends must be an error, not a truncated answer reported as complete")
	}
	// The cap is checked before each request, so exactly maxPages of them go out
	// and the one past the cap is refused. Pinning the count is what stops the
	// loop from quietly becoming unbounded, or from stopping one page early.
	if got := requests.Load(); got != 20 {
		t.Errorf("issued %d requests, want exactly maxPages (20)", got)
	}
}

func TestGitHub_PaginationNeverFollowsALinkOffTheConfiguredHost(t *testing.T) {
	// The next-page request carries the Authorization header, so a Link naming
	// another host would hand it the live forge token. Refusing is also the
	// only safe answer for recovery: silently stopping would report "no such
	// pull request" for a branch that has one.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("followed a Link onto another host, carrying Authorization %q", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `[]`)
	}))
	defer other.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<`+other.URL+`/repos/acme/widgets/pulls?page=2>; rel="next"`)
		_, _ = io.WriteString(w, `[{"number":1,"state":"open","head":{"sha":"a"},"base":{"ref":"main"}}]`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	if _, err := c.FindPullRequests(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "t"}, "caesar/abc"); err == nil {
		t.Fatal("want an error rather than a token handed to a stranger")
	}
}

func TestGitHub_TransportFailuresAreRetryable(t *testing.T) {
	// A dial failure is exactly the kind another attempt fixes, and the publish
	// machine only learns that through IsRetryable. A listener we close before
	// calling produces one without leaving the machine.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	c := forge.NewGitHub("http://"+addr, &http.Client{})
	_, err = c.Head(context.Background(), forge.Repo{Slug: "a/b", DefaultBranch: "main"}, forge.Auth{Token: "t"})
	if err == nil {
		t.Fatal("want a transport error")
	}
	if !forge.IsRetryable(err) {
		t.Errorf("a transport failure must be retryable, got %v", err)
	}
}
