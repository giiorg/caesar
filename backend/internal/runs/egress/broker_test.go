package egress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
