package httpx

import (
	"net/http"
	"strconv"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// LastEventIDHeader is the SSE resume header. A browser's EventSource sets it
// automatically from the last id: line it saw, so its value is normally one
// this server put on the wire.
const LastEventIDHeader = "Last-Event-ID"

// LastEventID reads a stream's resume point. The second return is whether the
// header was there at all: 0 is a legitimate value, not a sentinel, so a
// caller cannot tell "resume from the beginning" from "no resume" by the id
// alone.
//
// It lives in the shared kernel rather than in one api package because BOTH
// stream surfaces parse it and they sit in different bounded contexts, which
// cannot share code any other way. An absent header, and an empty one — which
// net/http cannot tell apart — are both "no resume".
//
// A value that is not a non-negative integer is a 400 rather than a silent
// fall back to the full snapshot: it can only come from a hand-rolled client,
// and a resume point quietly ignored looks to that client like a server that
// keeps re-sending history.
func LastEventID(r *http.Request) (int64, bool, error) {
	raw := r.Header.Get(LastEventIDHeader)
	if raw == "" {
		return 0, false, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, false, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{
				Field:   LastEventIDHeader,
				Message: "must be a non-negative integer",
			})
	}
	return id, true, nil
}
