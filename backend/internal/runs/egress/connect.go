package egress

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// connectPort is the only port a tunnel may reach: plain HTTP through the
// tunnel would be an unlogged, unbrokered exit, so TLS to a named host is
// the whole offer.
const (
	connectPort    = "443"
	connectPortNum = 443
)

// cgnat and ula are the two ranges Go's stdlib classifiers do not cover but
// that reach infrastructure just as well as RFC 1918 does.
var (
	cgnat = netip.MustParsePrefix("100.64.0.0/10")
	ula   = netip.MustParsePrefix("fc00::/7")
)

// serveConnect is the tunnel half of the gateway: authenticate, vet the
// target down to the resolved address, then splice bytes to the address the
// vetting approved.
func (g *Gateway) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port := splitTarget(r.Host)
	// Open mode: a gateway with no token digest holds no credential either
	// (NewGateway refuses any other combination), so its allowlist is the
	// only thing standing between the sandbox and the internet — and that is
	// exactly the posture package managers need, since they cannot be taught
	// a proxy credential.
	if g.cfg.TokenSHA256 != "" && !g.tokenMatches(bearerToken(r.Header.Get("Proxy-Authorization"))) {
		g.deny(w, http.StatusUnauthorized, "token", "invalid broker token", "host", host)
		return
	}
	addr, kind := g.vetConnect(r.Context(), host, port)
	if kind != "" {
		g.deny(w, http.StatusForbidden, kind, "connect target not allowed", "host", host, "port", port)
		return
	}

	// Dial before hijacking, so a dial failure is still an ordinary HTTP
	// response rather than a silently dropped connection.
	dialCtx, cancel := context.WithTimeout(r.Context(), dialTimeout)
	defer cancel()
	upstream, err := g.dial(dialCtx, addr.String())
	if err != nil {
		g.log.Warn("egress.connect_error", "attempt", g.cfg.Attempt, "host", host, "error", err.Error())
		writeJSONError(w, http.StatusBadGateway, "connect target unreachable")
		return
	}
	defer upstream.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "connection cannot be tunnelled")
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		g.log.Warn("egress.connect_error", "attempt", g.cfg.Attempt, "host", host, "error", err.Error())
		return
	}
	defer client.Close()
	// The server's header-read deadline must not survive into the tunnel.
	_ = client.SetDeadline(time.Time{})

	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	g.meter.connects.Add(1)
	g.log.Info("egress.connect", "attempt", g.cfg.Attempt, "host", host, "addr", addr.String())
	g.meter.connectBytes.Add(splice(client, buffered, upstream))
}

// splice copies both directions until either side hangs up and reports the
// total bytes tunnelled. Reads from the client come through the buffered
// reader, which may already hold bytes the client sent with its request.
func splice(client net.Conn, buffered io.Reader, upstream net.Conn) int64 {
	var total atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.CopyBuffer(upstream, buffered, make([]byte, copyBufferSize))
		total.Add(n)
		halfClose(upstream)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.CopyBuffer(client, upstream, make([]byte, copyBufferSize))
		total.Add(n)
		halfClose(client)
	}()
	wg.Wait()
	return total.Load()
}

// halfClose ends one direction so the peer sees a clean EOF; connections
// that cannot half-close (the test pipe) simply close.
func halfClose(c net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// vetConnect decides one tunnel and returns the address to dial. The kind it
// returns on refusal is the denial vocabulary the runner turns into run
// events: "port", "host", "private_ip", "resolve".
func (g *Gateway) vetConnect(ctx context.Context, host, port string) (netip.AddrPort, string) {
	if port != connectPort {
		return netip.AddrPort{}, "port"
	}
	if !g.hostAllowed(host) {
		return netip.AddrPort{}, "host"
	}
	addrs, err := g.resolver(ctx, host)
	if err != nil || len(addrs) == 0 {
		return netip.AddrPort{}, "resolve"
	}
	// Every answer must be public: one private address in the set means the
	// name is either misconfigured or hostile, and dialing any of them would
	// be a decision made on the strength of DNS.
	for _, a := range addrs {
		if !isPublicUnicast(a) {
			return netip.AddrPort{}, "private_ip"
		}
	}
	return netip.AddrPortFrom(addrs[0].Unmap(), connectPortNum), ""
}

func (g *Gateway) hostAllowed(host string) bool {
	for _, allowed := range g.cfg.ConnectAllow {
		if strings.EqualFold(allowed, host) {
			return true
		}
	}
	return false
}

// splitTarget parses a CONNECT authority. A target without a port yields an
// empty port, which the vetting refuses.
func splitTarget(target string) (host, port string) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return strings.TrimSuffix(target, ":"), ""
	}
	return host, port
}

// isPublicUnicast is the whole of the SSRF boundary: anything that is not a
// routable public address is refused, so a hostile or merely wrong DNS answer
// cannot point the tunnel at the host, the docker network, or the cloud
// metadata endpoint.
func isPublicUnicast(a netip.Addr) bool {
	a = a.Unmap() // an IPv4-mapped v6 address must be judged as the v4 address it is
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsLoopback() || a.IsPrivate() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	if a.Is4() && cgnat.Contains(a) {
		return false
	}
	if a.Is6() && ula.Contains(a) {
		return false
	}
	return true
}
