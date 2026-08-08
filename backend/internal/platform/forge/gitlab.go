package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GitLab speaks the REST API v4. baseURL is "https://gitlab.com" in production
// and an httptest URL in tests; a self-hosted instance is the same shape with
// a different base, which is why it is a field rather than a constant.
type GitLab struct {
	baseURL string
	hc      *http.Client
}

// The interface is not consumed inside this package, so this is what keeps a
// signature drift from surviving until app wiring.
var _ Client = (*GitLab)(nil)

func NewGitLab(baseURL string, hc *http.Client) *GitLab {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &GitLab{baseURL: strings.TrimRight(baseURL, "/"), hc: hc}
}

func (g *GitLab) headers(auth Auth) http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json")
	// GitLab reads a personal or project access token from PRIVATE-TOKEN, not
	// from Authorization: Bearer — the same token in the wrong header is an
	// anonymous request against a private project, which answers 404.
	h.Set("PRIVATE-TOKEN", auth.Token)
	return h
}

func (g *GitLab) do(ctx context.Context, op, method, u string, in, out any, auth Auth) (http.Header, error) {
	return roundTrip(ctx, g.hc, op, method, u, g.headers(auth), in, out, auth)
}

// project builds the project-scoped URL prefix. The project id in a path is
// the URL-ENCODED full path: GitLab projects nest, so group/sub/proj has to
// arrive as group%2Fsub%2Fproj or the request addresses a project named
// "group". This is why Repo.Slug may nest for GitLab and may not for GitHub.
func (g *GitLab) project(repo Repo) string {
	return g.baseURL + "/api/v4/projects/" + url.PathEscape(repo.Slug)
}

// gitlabMergeRequest is the slice of GitLab's merge-request document caesar
// records. Number comes from iid, never from id: iid is project-scoped and is
// what the web URL shows, while the instance-scoped id would link to a
// different project's merge request.
type gitlabMergeRequest struct {
	IID          int64  `json:"iid"`
	WebURL       string `json:"web_url"`
	State        string `json:"state"`
	SHA          string `json:"sha"`
	TargetBranch string `json:"target_branch"`
}

func (m gitlabMergeRequest) ref() PullRequestRef {
	return PullRequestRef{
		Number:  m.IID,
		URL:     m.WebURL,
		HeadSHA: m.SHA,
		Base:    m.TargetBranch,
		State:   m.State,
	}
}

func (g *GitLab) Head(ctx context.Context, repo Repo, auth Auth) (string, error) {
	sha, ok, err := g.branch(ctx, repo, auth, repo.DefaultBranch, "gitlab: resolve head")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("gitlab: resolve head: %q: %w", repo.DefaultBranch, ErrNotFound)
	}
	return sha, nil
}

func (g *GitLab) BranchHead(ctx context.Context, repo Repo, auth Auth, branch string) (string, bool, error) {
	return g.branch(ctx, repo, auth, branch, "gitlab: resolve branch")
}

// branch reads one branch's commit id. The sha lives at .commit.id here, not
// at .sha as it does on GitHub, and the branch name is escaped for the same
// reason the project path is: caesar/abc is one path segment.
func (g *GitLab) branch(ctx context.Context, repo Repo, auth Auth, branch, op string) (string, bool, error) {
	var out struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	u := g.project(repo) + "/repository/branches/" + url.PathEscape(branch)
	if _, err := g.do(ctx, op, http.MethodGet, u, nil, &out, auth); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return out.Commit.ID, true, nil
}

func (g *GitLab) CreatePullRequest(ctx context.Context, repo Repo, auth Auth, pr PullRequest) (PullRequestRef, error) {
	// GitLab's body spelling is not GitHub's: source_branch/target_branch/
	// description rather than head/base/body.
	in := map[string]string{
		"source_branch": pr.Head,
		"target_branch": pr.Base,
		"title":         pr.Title,
		"description":   pr.Body,
	}
	var out gitlabMergeRequest
	u := g.project(repo) + "/merge_requests"
	if _, err := g.do(ctx, "gitlab: create merge request", http.MethodPost, u, in, &out, auth); err != nil {
		return PullRequestRef{}, err
	}
	return out.ref(), nil
}

func (g *GitLab) FindPullRequests(ctx context.Context, repo Repo, auth Auth, headBranch string) ([]PullRequestRef, error) {
	const op = "gitlab: find merge requests"

	var refs []PullRequestRef
	page := ""
	for pages := 0; ; pages++ {
		if pages == maxPages {
			return nil, fmt.Errorf("%s: more than %d pages on %q; refusing a truncated answer", op, maxPages, headBranch)
		}
		q := url.Values{}
		q.Set("source_branch", headBranch)
		// A merged or closed request is still our receipt, so the probe must
		// see it.
		q.Set("state", "all")
		q.Set("per_page", strconv.Itoa(perPage))
		if page != "" {
			q.Set("page", page)
		}
		var listing []gitlabMergeRequest
		hdr, err := g.do(ctx, op, http.MethodGet, g.project(repo)+"/merge_requests?"+q.Encode(), nil, &listing, auth)
		if err != nil {
			return nil, err
		}
		for _, m := range listing {
			refs = append(refs, m.ref())
		}
		// GitLab paginates by X-Next-Page, not by GitHub's Link header, and
		// answers an empty one on the last page.
		page = strings.TrimSpace(hdr.Get("X-Next-Page"))
		if page == "" {
			return refs, nil
		}
	}
}
