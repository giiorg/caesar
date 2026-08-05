package dockerapi

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func TestDemuxLogs_SplitsStreamsAcrossChunkedFrames(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, "out-1 "))
	in.Write(frame(2, "err-1 "))
	in.Write(frame(1, "out-2"))
	var stdout, stderr strings.Builder
	// Feed one byte at a time to prove header/payload reassembly.
	if err := DemuxLogs(iotest.OneByteReader(&in), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "out-1 out-2" || stderr.String() != "err-1 " {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDemuxLogs_RejectsUnknownStreamByte(t *testing.T) {
	if err := DemuxLogs(bytes.NewReader(frame(7, "x")), io.Discard, io.Discard); err == nil {
		t.Fatal("want error on unknown stream id")
	}
}

func TestDemuxLogs_RejectsTornFrames(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{name: "header cut short", in: frame(1, "hello")[:5]},
		{name: "payload cut short", in: frame(1, "hello")[:10]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := DemuxLogs(bytes.NewReader(tt.in), io.Discard, io.Discard); err == nil {
				t.Fatal("want error on a torn frame")
			}
		})
	}
}
