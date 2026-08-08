package forge_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/forge"
)

func TestGitLab_HeadReadsTheBranchCommitID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The project id in the path is the URL-ENCODED full path: GitLab
		// projects nest, so g/p has to arrive as one segment.
		if got, want := r.URL.EscapedPath(), "/api/v4/projects/g%2Fp/repository/branches/main"; got != want {
			t.Errorf("escaped path = %q, want %q", got, want)
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "t" {
			t.Errorf("PRIVATE-TOKEN = %q; GitLab does not read Authorization: Bearer for a PAT", got)
		}
		// The sha lives at .commit.id here, not at .sha as it does on GitHub.
		_, _ = io.WriteString(w, `{"name":"main","commit":{"id":"deadbeef"}}`)
	}))
	defer srv.Close()

	c := forge.NewGitLab(srv.URL, srv.Client())
	sha, err := c.Head(context.Background(), forge.Repo{Slug: "g/p", DefaultBranch: "main"}, forge.Auth{Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if sha != "deadbeef" {
		t.Fatalf("Head = %q, want deadbeef", sha)
	}
}

func TestGitLab_BranchHeadReportsAbsenceRatherThanFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"404 Branch Not Found"}`)
	}))
	defer srv.Close()

	c := forge.NewGitLab(srv.URL, srv.Client())
	sha, ok, err := c.BranchHead(context.Background(), forge.Repo{Slug: "g/p"}, forge.Auth{Token: "t"}, "nope")
	if err != nil {
		t.Fatalf("an absent branch is an answer, not an error: %v", err)
	}
	if ok || sha != "" {
		t.Fatalf("BranchHead = %q,%v; want \"\",false", sha, ok)
	}
}

func TestGitLab_RecordsTheProjectScopedIIDAndTheTargetBranch(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The project id in the path is the URL-ENCODED full path: GitLab
		// projects nest, so group/sub/proj has to arrive as one segment.
		if got, want := r.URL.EscapedPath(), "/api/v4/projects/g%2Fp/merge_requests"; got != want {
			t.Errorf("escaped path = %q, want %q", got, want)
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "t" {
			t.Errorf("PRIVATE-TOKEN = %q; GitLab does not read Authorization: Bearer for a PAT", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":99999,"iid":7,"web_url":"https://gitlab.com/g/p/-/merge_requests/7","state":"opened","sha":"cafe","target_branch":"main"}`)
	}))
	defer srv.Close()

	c := forge.NewGitLab(srv.URL, srv.Client())
	got, err := c.CreatePullRequest(context.Background(), forge.Repo{Slug: "g/p"}, forge.Auth{Token: "t"},
		forge.PullRequest{Head: "caesar/abc", Base: "main", Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 7 {
		t.Fatalf("Number = %d, want the project-scoped iid 7 (id 99999 links to a different project's MR)", got.Number)
	}
	// Base is what makes a recovered candidate provable: matchPublication
	// treats an empty one as unprovable and halts the publication.
	if got.Base != "main" {
		t.Errorf("Base = %q, want main from .target_branch", got.Base)
	}
	if got.HeadSHA != "cafe" || got.URL == "" || got.State != "opened" {
		t.Errorf("receipt = %+v", got)
	}
	// GitLab's body spelling is not GitHub's; a copy-paste from github.go
	// sends head/base/body and GitLab silently opens an MR with no source.
	if body["source_branch"] != "caesar/abc" || body["target_branch"] != "main" || body["description"] != "b" {
		t.Errorf("request body = %v, want source_branch/target_branch/description", body)
	}
}

func TestGitLab_FindPullRequestsFollowsEveryPage(t *testing.T) {
	// GitLab paginates by X-Next-Page, not by GitHub's Link header: a loop
	// copied from github.go reads page one and stops, and recovery would miss
	// a candidate and open a second merge request for a spent approval.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/api/v4/projects/g%2Fp/merge_requests"; got != want {
			t.Errorf("escaped path = %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("source_branch"); got != "caesar/abc" {
			t.Errorf("source_branch = %q; GitLab filters by source, not by an owner-qualified head", got)
		}
		if got := r.URL.Query().Get("state"); got != "all" {
			t.Errorf("state = %q; a merged or closed MR is still OUR receipt", got)
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("X-Next-Page", "2")
			_, _ = io.WriteString(w, `[{"iid":1,"state":"merged","sha":"a","target_branch":"main"}]`)
		case "2":
			// GitLab answers an EMPTY X-Next-Page on the last page.
			w.Header().Set("X-Next-Page", "")
			_, _ = io.WriteString(w, `[{"iid":2,"state":"opened","sha":"b","target_branch":"main"}]`)
		default:
			t.Errorf("page %q requested after the last one; the loop is not terminating on X-Next-Page", r.URL.Query().Get("page"))
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer srv.Close()

	c := forge.NewGitLab(srv.URL, srv.Client())
	got, err := c.FindPullRequests(context.Background(), forge.Repo{Slug: "g/p"}, forge.Auth{Token: "t"}, "caesar/abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 2 {
		t.Fatalf("candidates = %+v, want both pages in order", got)
	}
	if got[0].Base != "main" || got[1].Base != "main" {
		t.Errorf("candidates = %+v, want every one carrying its target_branch", got)
	}
}
