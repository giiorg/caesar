package egress

import (
	"io"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
)

// hopByHop headers belong to one connection and never survive a hop.
// Proxy-Authorization is on the list twice over: it is hop-by-hop, and it
// carries the attempt's own token, which upstream must never see.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding",
	"TE", "Trailer", "Upgrade", "Proxy-Authenticate", "Proxy-Authorization",
}

// authHeaders are the ways an agent presents its attempt token. They are
// stripped before forwarding: the upstream sees the provider credential the
// gateway injects and nothing the sandbox chose.
var authHeaders = []string{"Authorization", "X-Api-Key", "Proxy-Authorization"}

// serveBroker is the credential half of the gateway: authenticate the
// attempt, check the operation against the allowlist, then forward with the
// provider credential swapped in.
func (g *Gateway) serveBroker(w http.ResponseWriter, r *http.Request) {
	g.meter.requests.Add(1)
	if g.cfg.Provider.Endpoint == "" {
		// A gateway with no provider brokers nothing at all — this is the
		// posture-validation gateway, whose only exit is its CONNECT
		// allowlist.
		g.deny(w, http.StatusForbidden, "no_provider", "this gateway brokers no provider",
			"method", r.Method, "path", r.URL.Path)
		return
	}
	if !g.tokenMatches(brokerToken(r.Header)) {
		g.deny(w, http.StatusUnauthorized, "token", "invalid broker token",
			"method", r.Method, "path", r.URL.Path)
		return
	}
	if !opAllowed(g.cfg.Provider.Ops, r.Method, r.URL.Path) {
		g.deny(w, http.StatusForbidden, "op", "operation not allowed",
			"method", r.Method, "path", r.URL.Path)
		return
	}
	g.forward(w, r)
}

// brokerToken reads the attempt token from whichever header the harness CLI
// natively uses for its "API key".
func brokerToken(h http.Header) string {
	if v := strings.TrimSpace(h.Get("X-Api-Key")); v != "" {
		return v
	}
	return bearerToken(h.Get("Authorization"))
}

// opAllowed matches method and path against the allowlist. The path must
// already be canonical: a request that only prefix-matches after traversal
// segments are resolved is a bypass attempt, not an operation.
func opAllowed(ops []Op, method, reqPath string) bool {
	if cleaned := path.Clean(reqPath); reqPath != cleaned && reqPath != cleaned+"/" {
		return false
	}
	for _, op := range ops {
		if strings.EqualFold(op.Method, method) && strings.HasPrefix(reqPath, op.PathPrefix) {
			return true
		}
	}
	return false
}

// forward builds the upstream request by hand. A reverse proxy would buffer
// where we must stream and would hide the byte counts the attempt is billed
// for, so the copy loop is explicit: flush after every chunk, count what
// crosses.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request) {
	target := g.cfg.Provider.Endpoint + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	body := &countingReader{src: r.Body}
	// An explicitly empty body is handed over as such rather than as a
	// wrapper the transport has to probe before it can decide not to chunk.
	var upstreamBody io.Reader = body
	if r.ContentLength == 0 {
		upstreamBody = http.NoBody
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, upstreamBody)
	if err != nil {
		g.log.Warn("egress.upstream_error", "attempt", g.cfg.Attempt, "stage", "build", "error", err.Error())
		writeJSONError(w, http.StatusBadGateway, "upstream request could not be built")
		return
	}
	copyHeaders(req.Header, r.Header)
	stripHopByHop(req.Header)
	for _, h := range authHeaders {
		req.Header.Del(h)
	}
	req.Header.Set(g.cfg.Provider.CredentialHeader, g.cfg.Provider.CredentialPrefix+g.cfg.Provider.Credential)
	req.ContentLength = r.ContentLength

	resp, err := g.upstream.Do(req)
	if err != nil {
		g.meter.requestBytes.Add(body.n.Load())
		g.log.Warn("egress.upstream_error", "attempt", g.cfg.Attempt, "stage", "roundtrip", "error", err.Error())
		writeJSONError(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	stripHopByHop(w.Header())
	w.WriteHeader(resp.StatusCode)
	written := streamBody(w, resp.Body)

	g.meter.requestBytes.Add(body.n.Load())
	g.meter.responseBytes.Add(written)
}

// streamBody copies upstream bytes out as they arrive, flushing each chunk so
// a server-sent-event stream reaches the agent token by token instead of at
// the end.
func streamBody(w http.ResponseWriter, src io.Reader) int64 {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, copyBufferSize)
	var total int64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := w.Write(buf[:n])
			total += int64(written)
			if flusher != nil {
				flusher.Flush()
			}
			if writeErr != nil {
				return total
			}
		}
		if readErr != nil {
			return total
		}
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// stripHopByHop removes the connection-scoped headers, including the ones
// the Connection header itself names.
func stripHopByHop(h http.Header) {
	for _, name := range strings.Split(h.Get("Connection"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			h.Del(name)
		}
	}
	for _, name := range hopByHop {
		h.Del(name)
	}
}

// countingReader bills the request body as the transport consumes it, which
// may be on another goroutine than the handler reading the count.
type countingReader struct {
	src io.Reader
	n   atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.n.Add(int64(n))
	return n, err
}
