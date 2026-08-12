// Package api is the realtime context's HTTP surface: one global stream of
// the attention events (gates, publications, run lifecycle) a caller may see.
// The per-run stream lives on the runs endpoint that already owns that URL and
// reaches this context through a port instead (runs/api.StreamServer).
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/realtime"
)

// StreamParams declares the resume header on the attention stream. The path
// carries no {id}, so this reaches the generator as the operation's Request
// with no requestOverrides entry of its own (internal/contracts).
type StreamParams struct {
	LastEventID string `header:"Last-Event-ID" description:"id of the last frame received; the stream resumes above it"`
}

type API struct {
	streams *realtime.Streamer
	auth    authn.SessionAuthenticator
	// grants resolves the caller's scope ONCE, at stream open, to build the
	// Filter. The Streamer re-resolves it on its own interval afterwards.
	grants realtime.GrantResolver
	log    *slog.Logger
}

// New builds the surface. It accepts nil dependencies because
// internal/contracts constructs this table with all of them nil for
// metadata-only generation and never runs a handler — exactly as it does for
// the peer contexts, whose nil services would panic just the same.
//
// Two of them are checked at request time rather than left to panic, and the
// asymmetry is deliberate: the Streamer and the GrantResolver are the ones
// internal/app can legitimately be mid-way through wiring, so a request finds
// them missing and says so (handleStreamAttention). A nil authenticator is not
// a state any wiring passes through, and it is not checked here for the same
// reason the peer packages do not check theirs.
func New(streams *realtime.Streamer, auth authn.SessionAuthenticator, grants realtime.GrantResolver, log *slog.Logger) *API {
	return &API{streams: streams, auth: auth, grants: grants, log: log}
}

// Operations is the route table: mux registration (httpx.Mount) and OpenAPI
// generation (internal/contracts) both consume it.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "GET /api/v1/stream", ID: "streamAttention",
			Summary: "Server-sent stream of attention events (gates, publications, run lifecycle) " +
				"on the projects the caller holds any grant on. Snapshot-then-deltas; " +
				"resume with Last-Event-ID. " +
				"Each frame's data: is one StreamFrame and its id: is the resume point. " +
				"A stream whose principal loses access to what it shows ends with a terminal " +
				"frame that carries NO id — `event: forbidden` with `data: {\"error\":\"forbidden\"}` " +
				"— and then closes: the 200 is committed with the first byte, so a revocation " +
				"can no longer be a 403.",
			Request:  StreamParams{},
			Response: realtime.StreamFrame{}, Status: http.StatusOK,
			ResponseContentType: "text/event-stream",
			Handler:             a.authenticated(a.handleStreamAttention),
		},
	}
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession)

// authenticated mirrors every other api package's wrapper (identity, assets,
// workflows, runs). On a stream it earns its keep twice over: it rejects
// BEFORE the handler runs, so nothing has committed a 200 and the client gets
// a real 401 problem+json rather than a stream that opens and says nothing.
func (a *API) authenticated(next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(authn.SessionCookie)
		if err != nil {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassUnauthenticated, "authentication required"))
			return
		}
		sess, err := a.auth.AuthenticateSession(r.Context(), c.Value)
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		next(w, r, sess)
	})
}

// handleStreamAttention opens the global stream.
//
// Everything that can still answer with a status happens first, in order, and
// the order is the point: the moment the Streamer builds its Writer the
// response is 200 forever, so a check deferred past that line can no longer be
// reported as anything but bytes in the body.
func (a *API) handleStreamAttention(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	// A dependency this surface cannot work without is a WIRING omission, and
	// the one thing it must never do is look like a working endpoint —
	// answering an empty stream would hide the omission behind a 200 nobody
	// would think to question. internal/app leaves the Streamer nil until
	// Task 11 builds the hub, tailer and streamer, so this branch is the live
	// answer between these two tasks rather than a defensive nicety.
	if a.streams == nil || a.grants == nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassInternal, "realtime streaming is not wired"))
		return
	}
	lastID, hasLastID, err := httpx.LastEventID(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	// The LAST thing that can be an HTTP status. A resolver failure here is
	// reported with whatever class it carries — a store outage is unclassified
	// and becomes a 500 — which is the same answer the poll endpoints give for
	// the same fault, and never a half-open stream.
	scope, err := a.grants.ScopeFor(r.Context(), sess.UserID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}

	// From here the response belongs to the Streamer — with ONE exception, and
	// it is the reason the exception is named rather than guessed at. Serve can
	// fail before it has committed anything, when the writer it was handed
	// cannot be flushed through its wrapper chain, and it reports that as
	// ErrNotStarted. That case is still a status, and it must be one: a stream
	// that could not open and answers a bare 200 is byte-for-byte what a
	// working stream with nothing to say yet looks like, so the failure would
	// never be seen. Every OTHER ending arrives over a committed 200 and can
	// only go to the log — calling Problem on those would append a problem
	// document to the SSE body, which is strictly worse than saying nothing.
	if err := a.serve(r.Context(), w, realtime.StreamRequest{
		Filter:      realtime.AttentionFilter(scope),
		UserID:      sess.UserID,
		LastEventID: lastID,
		HasLastID:   hasLastID,
	}, slog.String("stream", "attention"), slog.String("user_id", sess.UserID.String())); err != nil {
		// Unclassified, so httpx.Problem makes it a 500 and suppresses the
		// detail: "cannot flush" names a server-side wiring fault and is for
		// the log line serve already wrote, not for the client.
		httpx.Problem(w, r, err)
	}
}

// ServeRunStream is runs/api.StreamServer: the per-run stream, reached from
// the runs endpoint that already owns that URL. internal/app wires it in and
// asserts the signature, because runs and realtime are peers and neither may
// name the other.
//
// It is a method on this API rather than a second implementation in
// internal/app so that both surfaces share serve below and logStreamEnd —
// writing the ErrNotStarted branch and the six-shape classification a second
// time is how two surfaces that must agree stop agreeing.
//
// What it does NOT do is authenticate or check the run: runs/api has already
// resolved the run, checked the view grant and read Last-Event-ID, and hands
// the results in. What it does do is resolve the principal's SCOPE, which is
// realtime's own question and the last thing here that can still be an HTTP
// status.
func (a *API) ServeRunStream(ctx context.Context, w http.ResponseWriter,
	runID, userID uuid.UUID, lastEventID int64, hasLastID bool,
) error {
	// Same wiring omission, same refusal to look like a working endpoint, as
	// handleStreamAttention. Returned rather than rendered: the caller owns
	// the response until the stream commits it.
	if a.streams == nil || a.grants == nil {
		return xerrors.E(xerrors.ClassInternal, "realtime streaming is not wired")
	}
	scope, err := a.grants.ScopeFor(ctx, userID)
	if err != nil {
		return err
	}
	// RunFilter, never a Filter literal. Two of the four combinations of
	// RunID and Attention are wrong in ways nothing downstream can report,
	// and realtime.AttentionFilter names this exact call site as the risk.
	return a.serve(ctx, w, realtime.StreamRequest{
		Filter:      realtime.RunFilter(runID, scope),
		UserID:      userID,
		LastEventID: lastEventID,
		HasLastID:   hasLastID,
	}, slog.String("stream", "run"),
		slog.String("run_id", runID.String()),
		slog.String("user_id", userID.String()))
}

// serve runs one stream and TRANSLATES how it ended into what a caller that
// still owns the response may do about it. Both surfaces go through here, and
// the translation is the reason it exists.
//
// Serve's error set is wider than an HTTP surface's: it reports post-commit
// endings — a write failure, a source failure, CloseForbidden,
// CloseSlowConsumer, CloseShutdown, a grant re-check that failed often enough
// to end the stream — as errors too, because the caller inside realtime wants
// to know. Over HTTP those are not answerable: NewWriter committed the 200
// with the stream headers, and rendering problem+json on top of it appends a
// problem document to the SSE body, which is strictly worse than saying
// nothing.
//
// So exactly one error comes back out: ErrNotStarted, the one ending that
// wrote nothing at all. Everything else is logged at the level it deserves
// and reported as nil.
func (a *API) serve(ctx context.Context, w http.ResponseWriter, req realtime.StreamRequest, attrs ...slog.Attr) error {
	err := a.streams.Serve(ctx, w, req)
	logStreamEnd(ctx, a.log, err, attrs...)
	if errors.Is(err, realtime.ErrNotStarted) {
		return err
	}
	return nil
}

// logStreamEnd records how one stream ended, at the level the ending deserves.
// Serve has six shapes and two of them are faults:
//
//   - ErrNotStarted — the stream never opened. A wiring fault, error level, and
//     the only ending serve hands back for a caller to turn into a 500. Logged
//     as well as answered because the 500 carries no detail and the access log
//     carries no reason; the cause wrapped in here is the only place "cannot
//     flush" is written down.
//
//   - nil — the request's context was done, which for an SSE request normally
//     means the client disconnected. SILENT: AccessLog already writes a line
//     per request, and logging here would put a fault in the log for every
//     closed browser tab. Serve is explicit that a genuine write or source
//     failure RACING a disconnect also comes back nil and is therefore
//     swallowed here; that trade was made there, not here, and this cannot
//     undo it — the two are indistinguishable by the time they arrive.
//
//   - CloseSlowConsumer — the client fell behind and was evicted. Expected
//     traffic: it reconnects with Last-Event-ID and loses nothing.
//
//   - CloseShutdown — the process is going away.
//
//   - CloseForbidden — a revocation was acted on. Routine; a security event,
//     not an outage, and logging it as one would train a reader to ignore it.
//
//   - anything else — a write or source failure, or a grant re-check that
//     failed often enough to end the stream. The only error-level case.
//
// The reasons are matched with errors.Is against the CloseReason constants,
// which are comparable and match by value by design (hub.go). Never by string:
// the text is a message, not an identity.
//
// It does NOT try to say whether the process was shutting down. Serve returns
// nil, not CloseShutdown, when a disconnect races hub close, so the return
// value cannot carry that claim and this must not pretend it does.
func logStreamEnd(ctx context.Context, log *slog.Logger, err error, attrs ...slog.Attr) {
	if err == nil {
		return
	}
	level := slog.LevelError
	switch {
	case errors.Is(err, realtime.CloseSlowConsumer),
		errors.Is(err, realtime.CloseShutdown),
		errors.Is(err, realtime.CloseForbidden):
		level = slog.LevelInfo
	}
	log.LogAttrs(ctx, level, "sse stream ended", append(attrs, slog.String("reason", err.Error()))...)
}
