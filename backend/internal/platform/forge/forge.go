// Package forge is the shared-kernel client for the two code-hosting APIs
// caesar publishes to. It is deliberately narrow: four calls, hand-rolled
// over net/http against pinned API versions, no SDK, no domain types.
//
// Purity is load-bearing (internal/arch CheckPlatformPurity): this package
// imports no bounded context, so it speaks its own Repo/Auth/PullRequest
// vocabulary and app translates. That is the same shape platform/dockerapi
// has, for the same reason.
//
// Nothing here retries. Retry is a run-level decision with a budget and a
// deadline behind it, and burying it in a client would spend both invisibly.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Sentinels. Callers branch on these, never on status codes.
var (
	// ErrNotFound is a resource the forge says does not exist. Callers that
	// treat absence as an answer use the (value, ok, error) shape instead.
	ErrNotFound = errors.New("forge: not found")
	// ErrConflict is the forge refusing because the effect already exists or
	// contradicts the current state. Never retried — the publish machine's
	// recovery probe decides what it means.
	ErrConflict = errors.New("forge: conflict")
	// ErrUnauthorized is a credential the forge will not accept.
	ErrUnauthorized = errors.New("forge: unauthorized")
	// ErrForbidden is a credential the forge accepts and then will not let
	// act: an under-scoped fine-grained PAT, an org enforcing SAML SSO the
	// token is not authorized for, an IP allow-list, or an archived
	// repository. It is a SEPARATE sentinel from ErrUnauthorized because the
	// two are separate answers, and it exists at all because a plain error
	// here is read as "we could not see what happened" one layer up and
	// retried to the wall deadline — which no permission failure survives.
	//
	// It is NOT throttling. classify runs the rate-limit probe first, because
	// GitHub answers both a primary and a secondary rate limit with 403.
	ErrForbidden = errors.New("forge: forbidden")
)

// retryable marks a transport or 5xx/429 failure. A caller asks with
// IsRetryable rather than matching a type, so the classification can grow
// without changing call sites. `at` carries a reset the forge STATED, when it
// stated one.
type retryable struct {
	err error
	at  time.Time
}

func (r retryable) Error() string { return r.err.Error() }
func (r retryable) Unwrap() error { return r.err }

// IsRetryable reports whether another attempt could plausibly succeed.
func IsRetryable(err error) bool {
	var r retryable
	return errors.As(err, &r)
}

// RetryAfter returns the instant the forge said to come back, if it said one.
// The run layer prefers it over its own backoff curve: a provider's stated
// reset is truth, and waking before it retries into a window we were told is
// shut (the same rule retryDelay already applies to provider resets).
func RetryAfter(err error) (time.Time, bool) {
	var r retryable
	if errors.As(err, &r) && !r.at.IsZero() {
		return r.at, true
	}
	return time.Time{}, false
}

// rateLimited decides whether a response is throttling rather than refusal,
// and returns the reset instant when the forge supplied one.
//
// It exists because status code alone cannot answer the question on GitHub: a
// primary rate limit answers 403 or 429 with X-RateLimit-Remaining: 0 and an
// epoch-seconds X-RateLimit-Reset, and a secondary limit answers 403 or 429
// with Retry-After (delta-seconds) and a message naming the secondary limit.
// A 403 with none of those signals is a real permission failure and stays
// permanent — which is what keeps "the token cannot do this" from retrying
// until the wall deadline.
//
// GitLab is simpler and handled by the same helper: 429 with Retry-After
// (delta-seconds), or with the pair RateLimit-Remaining / RateLimit-Reset —
// which GitHub spells X-RateLimit-*, and which both forges send as EPOCH
// SECONDS, the form time.Unix reads below. The header name alone does not
// settle those units: the IETF draft that shares it specifies a delta, and
// GitLab's own delta/date sibling is RateLimit-ResetTime, which we do not read.
// Both spellings are read for BOTH halves of that pair: reading the reset
// un-prefixed but the remaining count prefixed would make the GitLab branch
// unreachable and silently discard every reset GitLab states.
func rateLimited(resp *http.Response, body []byte, now time.Time) (time.Time, bool) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	// Retry-After may legally be an HTTP-date. Neither forge sends one, and a
	// date fails Atoi silently and falls through to the reset header below —
	// which is deliberate, because the answer is still "retryable".
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return now.Add(time.Duration(secs) * time.Second), true
		}
	}
	reset := resp.Header.Get("X-RateLimit-Reset")
	if reset == "" {
		reset = resp.Header.Get("RateLimit-Reset")
	}
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if remaining == "" {
		remaining = resp.Header.Get("RateLimit-Remaining")
	}
	if remaining == "0" && reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			return time.Unix(epoch, 0).UTC(), true
		}
	}
	// 429 is throttling whether or not it explained itself; a bare 403 is not.
	if resp.StatusCode == http.StatusTooManyRequests {
		return time.Time{}, true
	}
	if bytes.Contains(bytes.ToLower(body), []byte("secondary rate limit")) {
		return time.Time{}, true
	}
	return time.Time{}, false
}

// Repo is a repository as the forge addresses it. Slug is "owner/name" for
// GitHub and the (possibly nested) project path for GitLab.
type Repo struct {
	Slug          string
	DefaultBranch string
}

// Auth carries the forge token for exactly one call. It is never stored on a
// client: the token is opened per use through the audited credential path,
// and a client that held one would outlive that audit.
type Auth struct{ Token string }

type PullRequest struct {
	Head  string
	Base  string
	Title string
	Body  string
}

// PullRequestRef is the receipt a publication records. State is the forge's
// own word ("open", "closed", "merged"); the publish machine records it and
// never interprets it beyond existence.
type PullRequestRef struct {
	Number  int64
	URL     string
	HeadSHA string
	// Base is the branch this pull request merges INTO. It is carried because
	// recovery locates a pull request by HEAD branch alone, and a head branch
	// is not a unique description of a publication: a human or a bot can open
	// a second request from our marker to a different target. Without the
	// base, recovery would adopt it and report a publication that merges
	// somewhere nobody approved.
	Base  string
	State string
}

// Client is the whole surface. Implemented by GitHub and GitLab.
type Client interface {
	// Head resolves the repository's default branch to a commit sha.
	Head(ctx context.Context, repo Repo, auth Auth) (string, error)
	// BranchHead resolves one branch; ok is false when it does not exist.
	BranchHead(ctx context.Context, repo Repo, auth Auth, branch string) (string, bool, error)
	// CreatePullRequest opens a PR/MR from pr.Head into pr.Base.
	CreatePullRequest(ctx context.Context, repo Repo, auth Auth, pr PullRequest) (PullRequestRef, error)
	// FindPullRequests returns every open-or-closed pull/merge request whose
	// head is this branch, paginated to exhaustion. It is the recovery probe
	// that turns "the receipt was lost" into "it already happened", and it
	// hands back all the candidates rather than picking one because the
	// caller decides ownership: a head branch alone does not identify a
	// publication. The same marker can carry several closed requests and
	// requests to different bases, and only the caller holds the intent to
	// compare them against.
	FindPullRequests(ctx context.Context, repo Repo, auth Auth, headBranch string) ([]PullRequestRef, error)
}

// defaultTimeout bounds one forge call. The publish machine also carries the
// run deadline on its context; this is the floor so a hung connection cannot
// hold a worker for the whole wall clock.
const defaultTimeout = 30 * time.Second

const (
	// maxBodyBytes caps every response read. A forge that answered without end
	// would otherwise be an unbounded allocation inside a worker.
	maxBodyBytes = 1 << 20
	// maxMessageBytes bounds how much of a forge's own words survive into an
	// error. The error lands in a run event and in publish_intents.halt_reason,
	// both durable, so a megabyte of HTML must not.
	maxMessageBytes = 200
)

const (
	// perPage asks for the largest page both forges allow, so exhaustion costs
	// one request in every plausible case.
	perPage = 100
	// maxPages bounds the recovery probe. Exhaustion is the contract, but a
	// server that answers every page identically must not be able to spin a
	// worker: 20 pages at 100 per page is far past any plausible number of
	// requests on one marker branch, and hitting it is a real error rather than
	// a truncated answer we might silently adopt.
	maxPages = 20
)

// forgeMessage decodes the forge's own explanation — GitHub answers
// {"message": …}, GitLab answers {"message": …} or {"error": …} and may put an
// object there — truncates it, and falls back to a fixed string when the body
// is not JSON at all. It reads only the response body: a request header never
// reaches it, and the token scrub is classify's job because only classify
// knows the token.
func forgeMessage(body []byte) string {
	var doc struct {
		Message json.RawMessage `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "unreadable forge response"
	}
	raw := doc.Message
	if len(raw) == 0 {
		raw = doc.Error
	}
	if len(raw) == 0 {
		return "forge gave no message"
	}
	msg := string(raw)
	// GitLab's message is sometimes an object or an array; the raw JSON is the
	// best rendering of it, so only the string form is unquoted.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		msg = s
	}
	if len(msg) > maxMessageBytes {
		msg = strings.ToValidUTF8(msg[:maxMessageBytes], "") + "…"
	}
	return msg
}

// redact removes the live credential from anything that becomes a durable
// string. A forge is free to reflect the Authorization header back in its
// error body, and this error lands in a run event and in
// publish_intents.halt_reason — so the scrub is on OUR side of the wire, not
// a promise we ask the forge to keep.
func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

// classify maps a response onto the sentinels. The rate-limit probe runs
// FIRST because status code alone cannot answer the question on GitHub: a 403
// with X-RateLimit-Remaining: 0, with Retry-After, or naming a secondary
// limit is throttling, and falling through to the generic 4xx arm would turn
// a few minutes of backoff into a failed run with a branch already pushed.
// A 403 with none of those signals is ErrForbidden, and it is a sentinel for
// the same reason ErrUnauthorized is: the composition root types anything it
// does not recognise as a transient, so a permission failure returned as a
// plain error retries to the wall deadline and then reports itself as an
// expiry after the branch was already pushed.
//
// The body is read for its message but never echoed past a bounded prefix,
// and the token is scrubbed from it: an error here lands in a run event and
// in publish_intents.halt_reason, both durable (spec §6 redaction).
func classify(op string, resp *http.Response, body []byte, token string, now time.Time) error {
	msg := redact(forgeMessage(body), token)
	if at, limited := rateLimited(resp, body, now); limited {
		return retryable{err: fmt.Errorf("%s: forge throttled with %d: %s", op, resp.StatusCode, msg), at: at}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%s: %w", op, ErrUnauthorized)
	// Reached only when rateLimited declined it above. The forge's own words
	// travel with it — "Resource protected by organization SAML enforcement",
	// "Repository was archived so is read-only" — because they are the whole
	// diagnosis for whoever has to fix the token or the repository.
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s: %w: %s", op, ErrForbidden, msg)
	case resp.StatusCode == http.StatusUnprocessableEntity, resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%s: %w: %s", op, ErrConflict, msg)
	case resp.StatusCode >= 500:
		return retryable{err: fmt.Errorf("%s: forge returned %d: %s", op, resp.StatusCode, msg)}
	case resp.StatusCode >= 400:
		return fmt.Errorf("%s: forge returned %d: %s", op, resp.StatusCode, msg)
	}
	return nil
}

// roundTrip performs one forge request and hands back the response headers —
// pagination is driven by them (GitHub's Link, GitLab's X-Next-Page), so a
// helper that returned only the decoded document could not exhaust a listing.
//
// It is shared by both clients because everything except the header set and
// the URL shape is identical between them: the deadline floor, the capped
// read, the classification and the decode.
func roundTrip(ctx context.Context, hc *http.Client, op, method, u string, hdr http.Header, in, out any, auth Auth) (http.Header, error) {
	// The caller's context carries the run deadline; this only lowers it, so a
	// connection that hangs cannot hold a worker for the whole wall clock.
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > defaultTimeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	req.Header = hdr.Clone()
	req.Header.Set("User-Agent", "caesar")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := hc.Do(req)
	if err != nil {
		// A dial or read failure is exactly the kind another attempt may fix.
		return nil, retryable{err: fmt.Errorf("%s: %w", op, err)}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return resp.Header, retryable{err: fmt.Errorf("%s: reading the forge's answer: %w", op, err)}
	}
	if err := classify(op, resp, raw, auth.Token, time.Now()); err != nil {
		return resp.Header, err
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.Header, fmt.Errorf("%s: decoding the forge's answer: %w", op, err)
		}
	}
	return resp.Header, nil
}
