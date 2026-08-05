package egress

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMeter_CountsRequestsDenialsAndForwardedBytes(t *testing.T) {
	const reply = `{"ok":true,"content":"hello"}`
	f := brokerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(reply))
	})
	const body = `{"model":"claude"}`

	brokerRequest(t, f.proxy, http.MethodPost, "/v1/messages", map[string]string{"x-api-key": tok}, body)
	brokerRequest(t, f.proxy, http.MethodDelete, "/v1/messages", map[string]string{"x-api-key": tok}, body)

	// The byte counters tick when the handler's copy loop finishes, which may
	// be after the client has read the whole response; only the final summary
	// is ordered after the handlers, so a live reading has to wait.
	want := meterSnapshot{Requests: 2, Denied: 1, RequestBytes: int64(len(body)), ResponseBytes: int64(len(reply))}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := f.gateway.meter.snapshot()
		if got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot = %+v, want %+v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMeter_CountsTunnelledBytes(t *testing.T) {
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
		t.Fatal(err)
	}
	if _, err := io.ReadFull(br, make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	// The tunnel's byte counts land when the copy loops finish, just after the
	// client hangs up.
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := g.meter.snapshot()
		if got.Connects == 1 && got.ConnectBytes == 8 { // "ping" each way
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot = %+v, want connects=1 connect_bytes=8", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRun_TicksTheMeterAndLogsAFinalSummary(t *testing.T) {
	g, logBuf := testGateway(t, Config{
		Attempt: "caesar-r-n-1", ListenAddr: "127.0.0.1:0", TokenSHA256: shaOf(tok),
	})
	g.meterInterval = 5 * time.Millisecond
	g.meter.requests.Add(3)
	g.meter.denied.Add(1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for lastRecord(t, logBuf, "egress.meter") == nil {
		if time.Now().After(deadline) {
			t.Fatalf("no egress.meter line within the deadline:\n%s", logBuf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	rec := lastRecord(t, logBuf, "egress.summary")
	if rec == nil {
		t.Fatalf("no egress.summary line:\n%s", logBuf.String())
	}
	// Task 8 harvests exactly these fields off the last summary line.
	want := map[string]float64{
		"requests": 3, "denied": 1, "request_bytes": 0, "response_bytes": 0, "connects": 0, "connect_bytes": 0,
	}
	for k, v := range want {
		got, ok := rec[k].(float64)
		if !ok {
			t.Errorf("summary is missing %q: %v", k, rec)
			continue
		}
		if got != v {
			t.Errorf("summary %q = %v, want %v", k, got, v)
		}
	}
	if rec["attempt"] != "caesar-r-n-1" {
		t.Errorf("summary attempt = %v, want the attempt ref", rec["attempt"])
	}
}

func TestRun_ReportsAnUnusableListenAddr(t *testing.T) {
	g, _ := testGateway(t, Config{Attempt: "a", ListenAddr: "127.0.0.1:-1", TokenSHA256: shaOf(tok)})
	err := g.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil for an unusable listen address")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Fatalf("error = %v, want it to name the listen failure", err)
	}
}
