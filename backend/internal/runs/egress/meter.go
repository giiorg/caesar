package egress

import (
	"log/slog"
	"sync/atomic"
)

// meter is the attempt's egress bill. Handlers on many connections update it
// concurrently, so every counter is atomic and snapshot() is the only reader.
type meter struct {
	requests      atomic.Int64 // broker requests handled, denied ones included
	denied        atomic.Int64 // refusals of either half of the gateway
	requestBytes  atomic.Int64 // bodies actually forwarded upstream
	responseBytes atomic.Int64 // bodies actually streamed back
	connects      atomic.Int64 // tunnels established
	connectBytes  atomic.Int64 // bytes tunnelled, both directions
}

// meterSnapshot is one consistent-enough reading: the counters are sampled
// independently, which is all a usage bill needs.
type meterSnapshot struct {
	Requests      int64
	Denied        int64
	RequestBytes  int64
	ResponseBytes int64
	Connects      int64
	ConnectBytes  int64
}

func (m *meter) snapshot() meterSnapshot {
	return meterSnapshot{
		Requests:      m.requests.Load(),
		Denied:        m.denied.Load(),
		RequestBytes:  m.requestBytes.Load(),
		ResponseBytes: m.responseBytes.Load(),
		Connects:      m.connects.Load(),
		ConnectBytes:  m.connectBytes.Load(),
	}
}

// logMeter writes one usage line. It takes the attempt and the counters and
// nothing else — the provider credential is not in its reach, which is how
// "the credential never appears in a log line" is enforced rather than
// remembered. The field names are a contract: the runner harvests the last
// egress.summary line off the gateway's log.
func logMeter(log *slog.Logger, msg, attempt string, s meterSnapshot) {
	log.Info(msg,
		"attempt", attempt,
		"requests", s.Requests,
		"denied", s.Denied,
		"request_bytes", s.RequestBytes,
		"response_bytes", s.ResponseBytes,
		"connects", s.Connects,
		"connect_bytes", s.ConnectBytes,
	)
}
