package api

import (
	"mime"
	"strconv"
	"strings"
)

// eventStreamType is the media type a browser's EventSource asks for, and the
// one this endpoint answers a stream with.
const eventStreamType = "text/event-stream"

// acceptsEventStream reports whether the client asked for an SSE stream on an
// endpoint that also answers JSON.
//
// It is a parser and not strings.Contains for one case that matters:
// "application/json, text/event-stream;q=0" NAMES the stream type in order to
// refuse it, and a substring test would hand that client a stream it said it
// did not want. So each range is parsed and a q of zero — or one that does not
// parse, which is a client saying something this server cannot honour — is a
// refusal.
//
// Everything else takes the JSON branch: absent, "*/*", "text/*" and any
// unrelated type. That direction is deliberate. The poll is the shipped
// contract on this URL and a client that never heard of streaming must keep
// getting it, while a browser's EventSource sends exactly "text/event-stream"
// and is never in doubt.
//
// Two ways this is deliberately not a full RFC 9110 negotiator, both of which
// the rules above would otherwise read as covering.
//
// RELATIVE PREFERENCE IS IGNORED. The only weights read are the refusing ones —
// a zero, a negative, or a q that does not parse at all; every other weight is
// treated as "yes". So "application/json;q=0.9,
// text/event-stream;q=0.1" streams, even though the client ranked JSON first.
// Ordering the ranges would mean scoring the JSON branch too — and "*/*"
// against an endpoint with two representations has no defensible winner —
// which is a negotiator, not the single-question test finding 9 asks for. No
// EventSource sends a weighted Accept, and a client that does is asking for
// something this endpoint has never offered.
//
// The comma split is not RFC-complete either: a quoted parameter value with a
// comma would be split through the middle. No Accept header in practice
// carries one, and the halves then fail to parse — so the failure lands on the
// JSON branch, which is the safe side.
func acceptsEventStream(header string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mt, params, err := mime.ParseMediaType(part)
		if err != nil || mt != eventStreamType {
			continue
		}
		q, ok := params["q"]
		if !ok {
			return true
		}
		weight, err := strconv.ParseFloat(q, 64)
		if err != nil || weight <= 0 {
			continue
		}
		return true
	}
	return false
}
