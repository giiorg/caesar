package egress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer captures the gateway's log while the gateway is still writing to
// it: the meter goroutine and the handlers outlive any single assertion.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func testGateway(t *testing.T, cfg Config) (*Gateway, *syncBuffer) {
	t.Helper()
	logBuf := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, nil))
	g, err := NewGateway(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	return g, logBuf
}

const tok = "cbt1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func shaOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// logRecords decodes every captured log line; a line that is not JSON is a
// bug in itself, since Task 8 parses this stream.
func logRecords(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// lastRecord returns the last captured line with the given msg, or nil.
func lastRecord(t *testing.T, buf *syncBuffer, msg string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, rec := range logRecords(t, buf) {
		if rec["msg"] == msg {
			found = rec
		}
	}
	return found
}

// waitForRecord waits for a line with the given msg to appear. The handler
// goroutine that logs a broken stream races the client's own read of it, so
// there is nothing to synchronise on but the log itself.
func waitForRecord(t *testing.T, buf *syncBuffer, msg string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rec := lastRecord(t, buf, msg); rec != nil {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s line after 5s:\n%s", msg, buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestServer(t *testing.T, g *Gateway) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(g.handler())
	t.Cleanup(srv.Close)
	return srv
}

// fixture is one gateway in the ordinary posture: a real provider behind it,
// an operation allowlist, and a token the agent must present.
type fixture struct {
	gateway  *Gateway
	log      *syncBuffer
	proxy    *httptest.Server // the gateway itself
	upstream *httptest.Server // the provider it fronts
}

func brokerFixture(t *testing.T, h http.HandlerFunc) fixture {
	t.Helper()
	upstream := httptest.NewServer(h)
	t.Cleanup(upstream.Close)
	g, logBuf := testGateway(t, Config{
		Attempt: "caesar-r-n-1", TokenSHA256: shaOf(tok),
		Provider: Provider{
			Endpoint: upstream.URL, Credential: "real-key", CredentialHeader: "x-api-key",
			Ops: []Op{{Method: "POST", PathPrefix: "/v1/messages"}, {Method: "GET", PathPrefix: "/v1/models"}},
		},
	})
	proxy := newTestServer(t, g)
	return fixture{gateway: g, log: logBuf, proxy: proxy, upstream: upstream}
}

// brokerRequest sends one request through the gateway under test.
func brokerRequest(t *testing.T, srv *httptest.Server, method, path string, headers map[string]string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func errorBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	return payload.Error
}

func TestBroker_InjectsCredentialAndStripsToken(t *testing.T) {
	var gotAuth, gotAPIKey, gotProxyAuth string
	f := brokerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAPIKey = r.Header.Get("Authorization"), r.Header.Get("x-api-key")
		gotProxyAuth = r.Header.Get("Proxy-Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	resp := brokerRequest(t, f.proxy, http.MethodPost, "/v1/messages?beta=1",
		map[string]string{"x-api-key": tok, "Authorization": "Bearer " + tok, "Proxy-Authorization": "Bearer " + tok}, `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotAPIKey != "real-key" {
		t.Errorf("upstream saw x-api-key = %q, want the injected credential", gotAPIKey)
	}
	if gotAuth != "" || gotProxyAuth != "" {
		t.Errorf("upstream saw authorization = %q proxy-authorization = %q, want both stripped", gotAuth, gotProxyAuth)
	}
}

func TestBroker_ForwardsPathQueryAndRewritesHost(t *testing.T) {
	var gotURI, gotHost, gotHop string
	var gotEncoding []string
	f := brokerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotHost, gotHop = r.RequestURI, r.Host, r.Header.Get("Keep-Alive")
		gotEncoding = r.TransferEncoding
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	wantHost := strings.TrimPrefix(f.upstream.URL, "http://")

	resp := brokerRequest(t, f.proxy, http.MethodGet, "/v1/models?limit=2",
		map[string]string{"x-api-key": tok, "Keep-Alive": "timeout=99"}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotURI != "/v1/models?limit=2" {
		t.Errorf("upstream request-uri = %q, want the original path and query", gotURI)
	}
	if gotHost != wantHost {
		t.Errorf("upstream Host = %q, want %q (rewritten to the endpoint)", gotHost, wantHost)
	}
	if gotHop != "" {
		t.Errorf("upstream saw hop-by-hop Keep-Alive = %q, want it dropped", gotHop)
	}
	if len(gotEncoding) != 0 {
		t.Errorf("upstream saw transfer-encoding %v, want a bodiless GET forwarded as-is", gotEncoding)
	}
	if got := resp.Header.Get("Keep-Alive"); got != "" {
		t.Errorf("client saw hop-by-hop Keep-Alive = %q, want it dropped", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("client saw Content-Type = %q, want the upstream's", got)
	}
}

func TestBroker_InvalidTokenIs401AndMetered(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"no credential at all", nil},
		{"wrong token in the native header", map[string]string{"x-api-key": "cbt1.wrong"}},
		{"wrong token as a bearer", map[string]string{"Authorization": "Bearer cbt1.wrong"}},
		{"empty bearer", map[string]string{"Authorization": "Bearer "}},
		{"the digest is not the token", map[string]string{"x-api-key": shaOf(tok)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := brokerFixture(t, func(http.ResponseWriter, *http.Request) {
				t.Error("upstream must never be reached by an unauthenticated request")
			})
			resp := brokerRequest(t, f.proxy, http.MethodPost, "/v1/messages", c.headers, `{}`)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if got := errorBody(t, resp); got != "invalid broker token" {
				t.Errorf("error = %q, want %q", got, "invalid broker token")
			}
			if got := f.gateway.meter.snapshot().Denied; got != 1 {
				t.Errorf("denied = %d, want 1", got)
			}
		})
	}
}

func TestBroker_DisallowedOperationIs403Logged(t *testing.T) {
	cases := []struct {
		name, method, path string
	}{
		{"method not on the allowlist", http.MethodDelete, "/v1/messages"},
		{"path not on the allowlist", http.MethodPost, "/v1/admin/keys"},
		{"prefix match must start at the path root", http.MethodPost, "/proxy/v1/messages"},
		{"traversal out of an allowed prefix", http.MethodPost, "/v1/messages/../../v1/admin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := brokerFixture(t, func(http.ResponseWriter, *http.Request) {
				t.Error("upstream must never be reached by a denied operation")
			})
			resp := brokerRequest(t, f.proxy, c.method, c.path, map[string]string{"x-api-key": tok}, `{}`)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
			rec := lastRecord(t, f.log, "egress.denied")
			if rec == nil {
				t.Fatalf("no egress.denied line in %s", f.log.String())
			}
			if rec["kind"] != "op" {
				t.Errorf("denied kind = %v, want %q", rec["kind"], "op")
			}
			if rec["method"] != c.method {
				t.Errorf("denied method = %v, want %q", rec["method"], c.method)
			}
			if got := f.gateway.meter.snapshot().Denied; got != 1 {
				t.Errorf("denied counter = %d, want 1", got)
			}
		})
	}
}

func TestBroker_BearerFormAccepted(t *testing.T) {
	cases := []struct{ name, header, value string }{
		{"native header", "x-api-key", tok},
		{"bearer", "Authorization", "Bearer " + tok},
		{"lowercase bearer", "Authorization", "bearer " + tok},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := brokerFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"ok":true}`))
			})
			resp := brokerRequest(t, f.proxy, http.MethodPost, "/v1/messages", map[string]string{c.header: c.value}, `{}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
		})
	}
}

func TestBroker_NoProviderModeRefusesAll(t *testing.T) {
	g, logBuf := testGateway(t, Config{
		Attempt: "caesar-r-n-1", TokenSHA256: shaOf(tok),
		ConnectAllow: []string{"registry.npmjs.org"},
	})
	srv := newTestServer(t, g)

	resp := brokerRequest(t, srv, http.MethodPost, "/v1/messages", map[string]string{"x-api-key": tok}, `{}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	rec := lastRecord(t, logBuf, "egress.denied")
	if rec == nil || rec["kind"] != "no_provider" {
		t.Fatalf("denied record = %v, want kind=no_provider", rec)
	}
}

func TestBroker_CredentialNeverLogged(t *testing.T) {
	const credential = "sk-ant-secret-value"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g, logBuf := testGateway(t, Config{
		Attempt: "caesar-r-n-1", TokenSHA256: shaOf(tok),
		Provider: Provider{
			Endpoint: upstream.URL, Credential: credential, CredentialHeader: "authorization", CredentialPrefix: "Bearer ",
			Ops: []Op{{Method: "POST", PathPrefix: "/v1/responses"}},
		},
	})
	srv := newTestServer(t, g)

	brokerRequest(t, srv, http.MethodPost, "/v1/responses", map[string]string{"x-api-key": tok}, `{}`)    // allowed
	brokerRequest(t, srv, http.MethodDelete, "/v1/responses", map[string]string{"x-api-key": tok}, `{}`)  // denied op
	brokerRequest(t, srv, http.MethodPost, "/v1/responses", map[string]string{"x-api-key": "nope"}, `{}`) // denied token
	logMeter(g.log, "egress.summary", g.cfg.Attempt, g.meter.snapshot())

	log := logBuf.String()
	for _, secret := range []string{credential, tok} {
		if strings.Contains(log, secret) {
			t.Fatalf("log leaks %q:\n%s", secret, log)
		}
	}
}

func TestBroker_StreamsResponseBodyWithoutBuffering(t *testing.T) {
	const chunk = "data: one\n\n"
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	f := brokerFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream writer is not a flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunk)
		flusher.Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
		flusher.Flush()
	})

	resp := brokerRequest(t, f.proxy, http.MethodPost, "/v1/messages", map[string]string{"x-api-key": tok}, `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	first := make(chan string, 1)
	go func() {
		b := make([]byte, len(chunk))
		if _, err := io.ReadFull(resp.Body, b); err != nil {
			first <- "read error: " + err.Error()
			return
		}
		first <- string(b)
	}()
	select {
	case got := <-first:
		if got != chunk {
			releaseOnce()
			t.Fatalf("first chunk = %q, want %q", got, chunk)
		}
	case <-time.After(5 * time.Second):
		releaseOnce()
		t.Fatal("first chunk never arrived: the gateway buffered the streamed response")
	}
	releaseOnce()
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the rest of the stream: %v", err)
	}
	if string(rest) != "data: two\n\n" {
		t.Fatalf("rest = %q, want the second chunk", rest)
	}
}

// TestBroker_TruncatedUpstreamStreamIsNotSilent is the production failure the
// flake investigation found: the provider's connection dies after the gateway
// has already written the response header and some bytes. The agent must not
// be handed a short answer that reads as a complete one.
func TestBroker_TruncatedUpstreamStreamIsNotSilent(t *testing.T) {
	const chunk = "data: one\n\n"
	f := brokerFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunk)
		w.(http.Flusher).Flush()
		// net/http drops the connection without writing the chunked
		// terminator, which is what a provider hanging up mid-stream looks
		// like on the wire.
		panic(http.ErrAbortHandler)
	})

	resp := brokerRequest(t, f.proxy, http.MethodPost, "/v1/messages", map[string]string{"x-api-key": tok}, `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("the agent read %q to a clean EOF; a broken stream must reach it as a transport error", got)
	}
	if string(got) != chunk {
		t.Errorf("delivered bytes = %q, want the chunk that crossed before the break", got)
	}

	rec := waitForRecord(t, f.log, "egress.upstream_error")
	if rec["stage"] != "stream" {
		t.Errorf("stage = %v, want %q", rec["stage"], "stream")
	}
	if rec["bytes"] != float64(len(chunk)) {
		t.Errorf("bytes = %v, want %d delivered before the break", rec["bytes"], len(chunk))
	}
	if got := f.gateway.meter.snapshot().ResponseBytes; got != int64(len(chunk)) {
		t.Errorf("metered response bytes = %d, want the %d that crossed", got, len(chunk))
	}
}

// TestBroker_ClientGoingAwayIsNotAnUpstreamFault pins the other side of the
// classification: the agent hanging up mid-stream is not a provider failure
// and must never be reported as one.
func TestBroker_ClientGoingAwayIsNotAnUpstreamFault(t *testing.T) {
	const chunk = "data: one\n\n"
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	f := brokerFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunk)
		w.(http.Flusher).Flush()
		<-release // the provider is mid-stream and perfectly healthy
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.proxy.URL+"/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", tok)
	resp, err := f.proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadFull(resp.Body, make([]byte, len(chunk))); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	cancel() // the agent goes away with the stream still open

	rec := waitForRecord(t, f.log, "egress.client_gone")
	if rec["stage"] != "stream" {
		t.Errorf("stage = %v, want %q", rec["stage"], "stream")
	}
	if rec := lastRecord(t, f.log, "egress.upstream_error"); rec != nil {
		t.Fatalf("the agent hanging up was logged as an upstream fault: %v", rec)
	}
}

// heldBody wraps the *http.body that net/http hands a handler as r.Body. It
// delegates every Read to that real body and adds exactly one power: the read
// that goes past Content-Length — the transport's unconditional check for
// extra bytes, transfer.go's doBodyCopy(io.Discard, body) — waits until the
// response is already on its way back to the client. That is the ordering the
// flake investigation measured happening by chance under CPU contention; the
// hold makes it certain. Only the transport's writeLoop goroutine reads this
// body: net/http's own drain reads the server's saved reference, not r.Body.
type heldBody struct {
	io.ReadCloser
	remaining int64 // Content-Length not yet handed to the reader
	hold      <-chan struct{}
}

func (b *heldBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		<-b.hold
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// TestBroker_InboundBodyIsNotReadAfterTheServerClosesIt drives the cause of
// the truncation rather than its silence. An upstream that answers before
// consuming the request body lets net/http's server reach its response-header
// path — which drains and CLOSES r.Body — while the transport still owes that
// body one read. Reading a closed server body kills the upstream connection
// mid-response, so the stream the agent gets is cut short.
func TestBroker_InboundBodyIsNotReadAfterTheServerClosesIt(t *testing.T) {
	const chunk = "data: one\n\n"
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	hold := make(chan struct{})
	holdOnce := sync.OnceFunc(func() { close(hold) })
	defer holdOnce()

	// This upstream answers on the headers alone, as a provider rejecting a
	// request does.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunk)
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(upstream.Close)

	g, logBuf := testGateway(t, Config{
		Attempt: "caesar-r-n-1", TokenSHA256: shaOf(tok),
		Provider: Provider{
			Endpoint: upstream.URL, Credential: "real-key", CredentialHeader: "x-api-key",
			Ops: []Op{{Method: "POST", PathPrefix: "/v1/messages"}},
		},
	})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = &heldBody{ReadCloser: r.Body, remaining: r.ContentLength, hold: hold}
		g.handler().ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)

	resp := brokerRequest(t, proxy, http.MethodPost, "/v1/messages", map[string]string{"x-api-key": tok}, `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, len(chunk))); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	// The response header is written, so net/http has already drained and
	// closed r.Body if it is going to. Now let the transport take its last
	// read.
	holdOnce()
	releaseOnce()

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the rest of the stream: %v\n%s", err, logBuf.String())
	}
	if string(rest) != "data: two\n\n" {
		t.Fatalf("rest = %q, want the second chunk\n%s", rest, logBuf.String())
	}
}

// unwrappedWriter is the ResponseWriter a middleware adds when it forwards
// every method but offers no Unwrap: ResponseController cannot reach
// net/http's own writer through it. It is strictly less capable than the
// *http.response production hands this handler — it flushes only because that
// writer does.
type unwrappedWriter struct {
	http.ResponseWriter
}

func (u unwrappedWriter) Flush() {
	if f, ok := u.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// TestBroker_AWriterWithoutFullDuplexIsSaidOutLoud covers the fallback: the
// gateway keeps brokering, but the request-body ownership it cannot claim is
// on the record instead of being assumed.
func TestBroker_AWriterWithoutFullDuplexIsSaidOutLoud(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)
	g, logBuf := testGateway(t, Config{
		Attempt: "caesar-r-n-1", TokenSHA256: shaOf(tok),
		Provider: Provider{
			Endpoint: upstream.URL, Credential: "real-key", CredentialHeader: "x-api-key",
			Ops: []Op{{Method: "POST", PathPrefix: "/v1/messages"}},
		},
	})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.handler().ServeHTTP(unwrappedWriter{ResponseWriter: w}, r)
	}))
	t.Cleanup(proxy.Close)

	resp := brokerRequest(t, proxy, http.MethodPost, "/v1/messages", map[string]string{"x-api-key": tok}, `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: the request is still brokered", resp.StatusCode)
	}
	rec := lastRecord(t, logBuf, "egress.full_duplex_unavailable")
	if rec == nil {
		t.Fatalf("no egress.full_duplex_unavailable line in %s", logBuf.String())
	}
	if rec["attempt"] != "caesar-r-n-1" {
		t.Errorf("attempt = %v, want the gateway's", rec["attempt"])
	}
}

// brokenPipeWriter stands in for the *http.response net/http hands this
// handler (gateway.go's http.Server): it is a flusher, as that writer always
// is, and its Write fails once the connection underneath has gone — the only
// way an agent that hung up shows up inside the copy loop. It can do nothing
// else that the real writer cannot.
type brokenPipeWriter struct {
	hdr       http.Header
	body      bytes.Buffer
	failAfter int // bytes accepted before writes start failing; -1 never fails
	flushes   int
}

func (b *brokenPipeWriter) Header() http.Header {
	if b.hdr == nil {
		b.hdr = http.Header{}
	}
	return b.hdr
}

func (b *brokenPipeWriter) WriteHeader(int) {}

func (b *brokenPipeWriter) Flush() { b.flushes++ }

func (b *brokenPipeWriter) Write(p []byte) (int, error) {
	if b.failAfter >= 0 && b.body.Len()+len(p) > b.failAfter {
		n, _ := b.body.Write(p[:max(0, b.failAfter-b.body.Len())])
		return n, errors.New("write tcp 127.0.0.1:1->127.0.0.1:2: broken pipe")
	}
	return b.body.Write(p)
}

// errorReader is the tail of a stream that failed: no bytes, one error.
type errorReader struct{ err error }

func (e errorReader) Read([]byte) (int, error) { return 0, e.err }

// TestStreamBody_ReportsHowTheCopyEnded pins the classification at its source:
// which of the two sides failed is what forward decides on, and a clean end of
// stream must stay indistinguishable from today's success.
func TestStreamBody_ReportsHowTheCopyEnded(t *testing.T) {
	const payload = "data: one\n\n"
	upstreamBroke := errors.New("read tcp 127.0.0.1:1->127.0.0.1:2: use of closed network connection")

	t.Run("a clean end of stream is not an error", func(t *testing.T) {
		w := &brokenPipeWriter{failAfter: -1}
		total, readErr, writeErr := streamBody(w, strings.NewReader(payload))
		if readErr != nil || writeErr != nil {
			t.Fatalf("read err = %v, write err = %v, want both nil at io.EOF", readErr, writeErr)
		}
		if total != int64(len(payload)) || w.body.String() != payload {
			t.Errorf("copied %d bytes %q, want all %d", total, w.body.String(), len(payload))
		}
		if w.flushes == 0 {
			t.Error("nothing was flushed: the stream would reach the agent only at the end")
		}
	})

	t.Run("an upstream failure is reported as one", func(t *testing.T) {
		w := &brokenPipeWriter{failAfter: -1}
		src := io.MultiReader(strings.NewReader(payload), errorReader{err: upstreamBroke})
		total, readErr, writeErr := streamBody(w, src)
		if !errors.Is(readErr, upstreamBroke) {
			t.Fatalf("read err = %v, want the upstream error", readErr)
		}
		if writeErr != nil {
			t.Errorf("write err = %v, want nil: the agent's side was fine", writeErr)
		}
		if total != int64(len(payload)) {
			t.Errorf("copied %d bytes, want the %d that crossed before the break", total, len(payload))
		}
	})

	t.Run("the agent's side failing is reported as the other thing", func(t *testing.T) {
		w := &brokenPipeWriter{failAfter: 4}
		rest := strings.NewReader("data: two\n\n")
		total, readErr, writeErr := streamBody(w, io.MultiReader(strings.NewReader(payload), rest))
		if writeErr == nil {
			t.Fatal("write err = nil, want the broken pipe")
		}
		if readErr != nil {
			t.Errorf("read err = %v, want nil: the provider never failed", readErr)
		}
		if total != 4 {
			t.Errorf("copied %d bytes, want the 4 the agent took", total)
		}
		if rest.Len() != len("data: two\n\n") {
			t.Errorf("%d bytes of the stream were still consumed after the agent went away", len("data: two\n\n")-rest.Len())
		}
	})
}

func TestBroker_UpstreamFailureIsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := upstream.URL
	upstream.Close() // nothing is listening any more

	g, _ := testGateway(t, Config{
		Attempt: "caesar-r-n-1", TokenSHA256: shaOf(tok),
		Provider: Provider{
			Endpoint: endpoint, Credential: "real-key", CredentialHeader: "x-api-key",
			Ops: []Op{{Method: "POST", PathPrefix: "/v1/messages"}},
		},
	})
	srv := newTestServer(t, g)

	resp := brokerRequest(t, srv, http.MethodPost, "/v1/messages", map[string]string{"x-api-key": tok}, `{}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}
