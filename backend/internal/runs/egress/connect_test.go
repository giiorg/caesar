package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// staticResolver answers every lookup with the same addresses, so the vetting
// tests never depend on DNS.
func staticResolver(t *testing.T, ips ...string) func(context.Context, string) ([]netip.Addr, error) {
	t.Helper()
	addrs := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		a, err := netip.ParseAddr(ip)
		if err != nil {
			t.Fatalf("bad test address %q: %v", ip, err)
		}
		addrs = append(addrs, a)
	}
	return func(context.Context, string) ([]netip.Addr, error) { return addrs, nil }
}

// dialRecorder is the dial seam: it records what the gateway actually dialed
// and answers with an echo pipe, so no test touches the network.
type dialRecorder struct {
	mu    sync.Mutex
	addrs []string
}

func (d *dialRecorder) dial(_ context.Context, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, addr)
	d.mu.Unlock()
	client, server := net.Pipe()
	go func() {
		_, _ = io.Copy(server, server)
		_ = server.Close()
	}()
	return client, nil
}

func (d *dialRecorder) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.addrs)
}

// connectRequest performs a raw CONNECT handshake against the gateway and
// returns the still-open connection, its buffered reader (which may already
// hold tunnel bytes), and the response status line.
func connectRequest(t *testing.T, srv *httptest.Server, target string, headers map[string]string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dialing the gateway: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var req strings.Builder
	fmt.Fprintf(&req, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	for k, v := range headers {
		fmt.Fprintf(&req, "%s: %s\r\n", k, v)
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(conn, req.String()); err != nil {
		t.Fatalf("writing the CONNECT request: %v", err)
	}
	br := bufio.NewReader(conn)
	tp := textproto.NewReader(br)
	status, err := tp.ReadLine()
	if err != nil {
		t.Fatalf("reading the status line: %v", err)
	}
	if _, err := tp.ReadMIMEHeader(); err != nil {
		t.Fatalf("reading the response headers: %v", err)
	}
	return conn, br, status
}

func TestVetTarget_Table(t *testing.T) {
	cases := []struct {
		name       string
		host, port string
		ips        []string
		wantKind   string // "" = allowed
	}{
		{"allowlisted host on 443", "registry.npmjs.org", "443", []string{"104.16.0.1"}, ""},
		{"host match is case-insensitive", "REGISTRY.NPMJS.ORG", "443", []string{"104.16.0.1"}, ""},
		{"host not on the allowlist", "evil.example", "443", []string{"104.16.0.1"}, "host"},
		{"a suffix of an allowed host is not allowed", "evilregistry.npmjs.org", "443", []string{"104.16.0.1"}, "host"},
		{"non-TLS port", "registry.npmjs.org", "80", []string{"104.16.0.1"}, "port"},
		{"no port at all", "registry.npmjs.org", "", []string{"104.16.0.1"}, "port"},
		{"rfc1918", "registry.npmjs.org", "443", []string{"10.0.0.5"}, "private_ip"},
		{"loopback", "registry.npmjs.org", "443", []string{"127.0.0.1"}, "private_ip"},
		{"loopback v6", "registry.npmjs.org", "443", []string{"::1"}, "private_ip"},
		{"cloud metadata endpoint", "registry.npmjs.org", "443", []string{"169.254.169.254"}, "private_ip"},
		{"cgnat", "registry.npmjs.org", "443", []string{"100.64.0.9"}, "private_ip"},
		{"unique local v6", "registry.npmjs.org", "443", []string{"fd00::1"}, "private_ip"},
		{"link-local v6", "registry.npmjs.org", "443", []string{"fe80::1"}, "private_ip"},
		{"unspecified", "registry.npmjs.org", "443", []string{"0.0.0.0"}, "private_ip"},
		{"multicast", "registry.npmjs.org", "443", []string{"224.0.0.1"}, "private_ip"},
		{"ipv4-mapped private address", "registry.npmjs.org", "443", []string{"::ffff:169.254.169.254"}, "private_ip"},
		{"any private address taints the target", "registry.npmjs.org", "443", []string{"104.16.0.1", "10.0.0.5"}, "private_ip"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, _ := testGateway(t, Config{Attempt: "a", TokenSHA256: shaOf(tok), ConnectAllow: []string{"registry.npmjs.org"}})
			g.resolver = staticResolver(t, c.ips...)
			addr, kind := g.vetConnect(context.Background(), c.host, c.port)
			if kind != c.wantKind {
				t.Fatalf("vetConnect(%q, %q) kind = %q, want %q", c.host, c.port, kind, c.wantKind)
			}
			if kind == "" {
				if want := c.ips[0] + ":443"; addr.String() != want {
					t.Errorf("vetted address = %q, want %q", addr.String(), want)
				}
			}
		})
	}
}

func TestVetTarget_UnresolvableHostIsDenied(t *testing.T) {
	cases := []struct {
		name     string
		resolver func(context.Context, string) ([]netip.Addr, error)
	}{
		{"lookup fails", func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("nxdomain") }},
		{"lookup is empty", func(context.Context, string) ([]netip.Addr, error) { return nil, nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, _ := testGateway(t, Config{Attempt: "a", TokenSHA256: shaOf(tok), ConnectAllow: []string{"allowed.test"}})
			g.resolver = c.resolver
			if _, kind := g.vetConnect(context.Background(), "allowed.test", "443"); kind != "resolve" {
				t.Fatalf("kind = %q, want %q", kind, "resolve")
			}
		})
	}
}

func TestConnect_TunnelsBytesToVettedAddrOnly(t *testing.T) {
	g, _ := testGateway(t, Config{Attempt: "a", TokenSHA256: shaOf(tok), ConnectAllow: []string{"allowed.test"}})
	g.resolver = staticResolver(t, "93.184.216.34")
	dialer := &dialRecorder{}
	g.dial = dialer.dial
	srv := newTestServer(t, g)

	conn, br, status := connectRequest(t, srv, "allowed.test:443", map[string]string{"Proxy-Authorization": "Bearer " + tok})
	if status != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("status line = %q", status)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatalf("writing into the tunnel: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("reading back from the tunnel: %v", err)
	}
	if string(got) != "ping" {
		t.Errorf("tunnel echoed %q, want %q", got, "ping")
	}
	if want := []string{"93.184.216.34:443"}; !slices.Equal(dialer.dialed(), want) {
		t.Errorf("dialed %v, want %v (the vetted address, never the hostname)", dialer.dialed(), want)
	}
	_ = conn.Close()
}

func TestConnect_DeniedIsLoggedWithHost(t *testing.T) {
	g, logBuf := testGateway(t, Config{Attempt: "a", TokenSHA256: shaOf(tok), ConnectAllow: []string{"allowed.test"}})
	g.resolver = staticResolver(t, "93.184.216.34")
	dialer := &dialRecorder{}
	g.dial = dialer.dial
	srv := newTestServer(t, g)

	_, _, status := connectRequest(t, srv, "other.test:443", map[string]string{"Proxy-Authorization": "Bearer " + tok})
	if !strings.HasPrefix(status, "HTTP/1.1 403") {
		t.Fatalf("status line = %q, want 403", status)
	}
	rec := lastRecord(t, logBuf, "egress.denied")
	if rec == nil {
		t.Fatalf("no egress.denied line in %s", logBuf.String())
	}
	if rec["kind"] != "host" || rec["host"] != "other.test" {
		t.Errorf("denied record = %v, want kind=host host=other.test", rec)
	}
	if len(dialer.dialed()) != 0 {
		t.Errorf("dialed %v, want nothing", dialer.dialed())
	}
	if got := g.meter.snapshot().Denied; got != 1 {
		t.Errorf("denied = %d, want 1", got)
	}
}

func TestConnect_RequiresProxyAuth(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"no proxy credential", nil},
		{"wrong proxy credential", map[string]string{"Proxy-Authorization": "Bearer cbt1.wrong"}},
		{"the agent's own header is not the proxy credential", map[string]string{"x-api-key": tok}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, _ := testGateway(t, Config{Attempt: "a", TokenSHA256: shaOf(tok), ConnectAllow: []string{"allowed.test"}})
			g.resolver = staticResolver(t, "93.184.216.34")
			dialer := &dialRecorder{}
			g.dial = dialer.dial
			srv := newTestServer(t, g)

			_, _, status := connectRequest(t, srv, "allowed.test:443", c.headers)
			if !strings.HasPrefix(status, "HTTP/1.1 401") {
				t.Fatalf("status line = %q, want 401", status)
			}
			if len(dialer.dialed()) != 0 {
				t.Errorf("dialed %v, want nothing", dialer.dialed())
			}
		})
	}
}

func TestConnect_OpenModeSkipsAuthWhenNoProviderConfigured(t *testing.T) {
	g, logBuf := testGateway(t, Config{Attempt: "a", ConnectAllow: []string{"allowed.test"}})
	g.resolver = staticResolver(t, "93.184.216.34")
	dialer := &dialRecorder{}
	g.dial = dialer.dial
	srv := newTestServer(t, g)

	conn, br, status := connectRequest(t, srv, "allowed.test:443", nil)
	if status != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("status line = %q, want the tunnel to open without a credential", status)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatalf("writing into the tunnel: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("reading back from the tunnel: %v", err)
	}
	_ = conn.Close()

	// The same gateway still brokers nothing: it holds no credential.
	resp := brokerRequest(t, srv, http.MethodPost, "/v1/messages", nil, `{}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("broker status = %d, want 403", resp.StatusCode)
	}
	rec := lastRecord(t, logBuf, "egress.denied")
	if rec == nil || rec["kind"] != "no_provider" {
		t.Fatalf("denied record = %v, want kind=no_provider", rec)
	}
}

func TestNewGateway_RejectsUnrunnableConfigurations(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			"an unauthenticated broker holding a provider credential",
			Config{Attempt: "a", Provider: Provider{Endpoint: "https://api.anthropic.com", Credential: "real-key", CredentialHeader: "x-api-key"}},
		},
		{
			"a meter that would never tick",
			Config{Attempt: "a", TokenSHA256: shaOf(tok), MeterSeconds: -1},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewGateway(c.cfg, slog.New(slog.NewJSONHandler(io.Discard, nil))); err == nil {
				t.Fatal("NewGateway accepted it")
			}
		})
	}
}
