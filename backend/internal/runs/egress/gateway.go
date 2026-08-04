package egress

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	// upstreamResponseHeaderTimeout bounds the wait for a provider's response
	// headers. There is deliberately no overall timeout: a long completion
	// streams for minutes and cutting it would look like a provider failure.
	upstreamResponseHeaderTimeout = 60 * time.Second
	dialTimeout                   = 10 * time.Second
	readHeaderTimeout             = 30 * time.Second
	shutdownTimeout               = 5 * time.Second
	copyBufferSize                = 32 << 10
)

// Gateway is one attempt's dual-homed egress process: a credential broker on
// the ordinary HTTP path and a vetting CONNECT proxy on the tunnel path,
// sharing one listener, one token, and one meter.
type Gateway struct {
	cfg   Config
	log   *slog.Logger
	meter meter

	// Seams. The resolver and dialer are split so vetting can be tested
	// without DNS or the network, and so the tunnel dials the address the
	// vetting approved rather than resolving a second time.
	resolver      func(ctx context.Context, host string) ([]netip.Addr, error)
	dial          func(ctx context.Context, addrPort string) (net.Conn, error)
	upstream      *http.Client
	meterInterval time.Duration
}

// NewGateway prepares the gateway. It refuses the one combination that would
// turn the posture inside out: an unauthenticated broker (no token digest)
// that nonetheless holds a provider credential. Open mode exists only for
// gateways that carry nothing worth stealing.
func NewGateway(cfg Config, log *slog.Logger) (*Gateway, error) {
	cfg.applyDefaults()
	if cfg.TokenSHA256 == "" && cfg.Provider.Endpoint != "" {
		return nil, errors.New("egress: a gateway without a token digest may not hold a provider credential")
	}
	if cfg.MeterSeconds <= 0 {
		return nil, errors.New("egress: meter_seconds must be positive")
	}
	resolver := &net.Resolver{}
	dialer := &net.Dialer{Timeout: dialTimeout}
	return &Gateway{
		cfg: cfg,
		log: log,
		resolver: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return resolver.LookupNetIP(ctx, "ip", host)
		},
		dial: func(ctx context.Context, addrPort string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", addrPort)
		},
		upstream: &http.Client{
			// A redirect is the provider asking us to send the credential
			// somewhere else; hand the response back instead and let the
			// caller decide.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				Proxy:                 nil, // this process IS the proxy
				DialContext:           dialer.DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConnsPerHost:   16,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: upstreamResponseHeaderTimeout,
			},
		},
		meterInterval: time.Duration(cfg.MeterSeconds) * time.Second,
	}, nil
}

func (g *Gateway) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			g.serveConnect(w, r)
			return
		}
		g.serveBroker(w, r)
	})
}

// Run serves until the context is done, then shuts down and writes the final
// usage summary. The summary is the last line of the gateway's log by
// construction: the runner reads it back as the attempt's egress bill.
func (g *Gateway) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", g.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("egress: listen %s: %w", g.cfg.ListenAddr, err)
	}
	srv := &http.Server{Handler: g.handler(), ReadHeaderTimeout: readHeaderTimeout}
	g.log.Info("egress.start",
		"attempt", g.cfg.Attempt,
		"addr", ln.Addr().String(),
		"broker", g.cfg.Provider.Endpoint != "",
		"ops", len(g.cfg.Provider.Ops),
		"connect_allow", g.cfg.ConnectAllow,
	)

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	ticker := time.NewTicker(g.meterInterval)
	defer ticker.Stop()

	var serveErr error
loop:
	for {
		select {
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			shutErr := srv.Shutdown(shutCtx)
			cancel()
			if shutErr != nil {
				_ = srv.Close() // a stream still in flight; the attempt is over regardless
			}
			<-served
			break loop
		case err := <-served:
			if !errors.Is(err, http.ErrServerClosed) {
				serveErr = fmt.Errorf("egress: serve: %w", err)
			}
			break loop
		case <-ticker.C:
			logMeter(g.log, "egress.meter", g.cfg.Attempt, g.meter.snapshot())
		}
	}
	logMeter(g.log, "egress.summary", g.cfg.Attempt, g.meter.snapshot())
	return serveErr
}

// tokenMatches compares the presented attempt token against the configured
// digest in constant time. An empty configured digest matches nothing: open
// mode is a decision the CONNECT path makes explicitly, never a comparison
// that happens to succeed.
func (g *Gateway) tokenMatches(presented string) bool {
	if g.cfg.TokenSHA256 == "" || presented == "" {
		return false
	}
	sum := sha256.Sum256([]byte(presented))
	digest := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(digest), []byte(g.cfg.TokenSHA256)) == 1
}

// bearerToken reads a "Bearer <token>" header value, accepting the casing
// variations real CLIs emit.
func bearerToken(v string) string {
	const scheme = "bearer "
	if len(v) < len(scheme) || !strings.EqualFold(v[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(v[len(scheme):])
}

// deny refuses one request: it meters the refusal, logs the kind the runner
// turns into a phase.egress_denied event, and answers in JSON. Its arguments
// are the fields it logs and nothing more, so no caller can hand it the
// credential.
func (g *Gateway) deny(w http.ResponseWriter, status int, kind, message string, attrs ...any) {
	g.meter.denied.Add(1)
	g.log.Warn("egress.denied", append([]any{"attempt", g.cfg.Attempt, "kind", kind}, attrs...)...)
	writeJSONError(w, status, message)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: message})
}
