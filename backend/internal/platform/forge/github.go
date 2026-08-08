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

// GitHub speaks the REST API at a pinned version. baseURL is
// "https://api.github.com" in production and an httptest URL in tests;
// self-hosted GitHub Enterprise is the same shape with a different base,
// which is why it is a field rather than a constant.
type GitHub struct {
	baseURL string
	hc      *http.Client
}

// The interface is not consumed inside this package, so this is what keeps a
// signature drift from surviving until app wiring.
var _ Client = (*GitHub)(nil)

func NewGitHub(baseURL string, hc *http.Client) *GitHub {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &GitHub{baseURL: strings.TrimRight(baseURL, "/"), hc: hc}
}

func (g *GitHub) headers(auth Auth) http.Header {
	h := http.Header{}
	h.Set("Accept", "application/vnd.github+json")
	// Pinning the version is what stops a silent response-shape change from
	// becoming a silent publish failure.
	h.Set("X-GitHub-Api-Version", "2022-11-28")
	h.Set("Authorization", "Bearer "+auth.Token)
	return h
}

func (g *GitHub) do(ctx context.Context, op, method, u string, in, out any, auth Auth) (http.Header, error) {
	return roundTrip(ctx, g.hc, op, method, u, g.headers(auth), in, out, auth)
}

// githubPullRequest is the slice of GitHub's pull-request document caesar
// records. Both the create response and every listing element have this shape,
// so the mapping onto a receipt lives in one place.
type githubPullRequest struct {
	Number  int64  `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Head    struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p githubPullRequest) ref() PullRequestRef {
	return PullRequestRef{
		Number:  p.Number,
		URL:     p.HTMLURL,
		HeadSHA: p.Head.SHA,
		Base:    p.Base.Ref,
		State:   p.State,
	}
}

func (g *GitHub) Head(ctx context.Context, repo Repo, auth Auth) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	u := g.baseURL + "/repos/" + repo.Slug + "/commits/" + url.PathEscape(repo.DefaultBranch)
	if _, err := g.do(ctx, "github: resolve head", http.MethodGet, u, nil, &out, auth); err != nil {
		return "", err
	}
	return out.SHA, nil
}

func (g *GitHub) BranchHead(ctx context.Context, repo Repo, auth Auth, branch string) (string, bool, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	// The whole branch is one escaped segment: git/ref/heads/{ref} treats
	// everything after heads/ as the ref, so caesar/abc has to travel as
	// caesar%2Fabc or it addresses a different ref.
	u := g.baseURL + "/repos/" + repo.Slug + "/git/ref/heads/" + url.PathEscape(branch)
	if _, err := g.do(ctx, "github: resolve branch", http.MethodGet, u, nil, &out, auth); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return out.Object.SHA, true, nil
}

func (g *GitHub) CreatePullRequest(ctx context.Context, repo Repo, auth Auth, pr PullRequest) (PullRequestRef, error) {
	in := map[string]string{
		"head":  pr.Head,
		"base":  pr.Base,
		"title": pr.Title,
		"body":  pr.Body,
	}
	var out githubPullRequest
	u := g.baseURL + "/repos/" + repo.Slug + "/pulls"
	if _, err := g.do(ctx, "github: create pull request", http.MethodPost, u, in, &out, auth); err != nil {
		return PullRequestRef{}, err
	}
	return out.ref(), nil
}

func (g *GitHub) FindPullRequests(ctx context.Context, repo Repo, auth Auth, headBranch string) ([]PullRequestRef, error) {
	const op = "github: find pull requests"

	// GitHub's head filter is owner-qualified, and the owner comes from the
	// slug — so a slug that is not exactly owner/name cannot build the probe,
	// and guessing would silently list somebody else's repository.
	owner, name, ok := strings.Cut(repo.Slug, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("%s: %q is not an owner/name slug", op, repo.Slug)
	}

	q := url.Values{}
	q.Set("head", owner+":"+headBranch)
	// A merged or closed request is still our receipt, so the probe must see it.
	q.Set("state", "all")
	q.Set("per_page", strconv.Itoa(perPage))
	next := g.baseURL + "/repos/" + repo.Slug + "/pulls?" + q.Encode()

	var refs []PullRequestRef
	for pages := 0; next != ""; pages++ {
		if pages == maxPages {
			return nil, fmt.Errorf("%s: more than %d pages on %q; refusing a truncated answer", op, maxPages, headBranch)
		}
		var page []githubPullRequest
		hdr, err := g.do(ctx, op, http.MethodGet, next, nil, &page, auth)
		if err != nil {
			return nil, err
		}
		for _, p := range page {
			refs = append(refs, p.ref())
		}
		next = nextLink(hdr.Get("Link"))
		// The next request carries the token, so a Link naming another host
		// would hand a live credential to it. Refusing beats truncating: a
		// probe that stopped quietly would report no pull request for a branch
		// that has one, and recovery would open a second.
		if next != "" && !strings.HasPrefix(next, g.baseURL+"/") {
			return nil, fmt.Errorf("%s: the next-page link left %s", op, g.baseURL)
		}
	}
	return refs, nil
}

// nextLink returns the rel="next" URL out of a GitHub Link header, or "" when
// there is no next page — which is how the listing loop learns it is done.
// GitHub paginates by this header rather than by a page count, and following
// it is the only termination condition that does not depend on guessing how
// the server treats an out-of-range page.
func nextLink(header string) string {
	for _, link := range strings.Split(header, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 {
			continue
		}
		target := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, param := range parts[1:] {
			if strings.EqualFold(strings.TrimSpace(param), `rel="next"`) {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}
