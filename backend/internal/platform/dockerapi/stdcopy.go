package dockerapi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// DemuxLogs splits a container's log stream into stdout and stderr. With TTY
// off the daemon multiplexes both over one connection: an 8-byte header
// {stream, 0,0,0, len:be32} then that many payload bytes.
//
// A clean end of stream between frames returns nil; a frame torn mid-header
// or mid-payload is an error, because silently truncated agent output would
// read as agent output.
func DemuxLogs(r io.Reader, stdout, stderr io.Writer) error {
	head := make([]byte, 8)
	buf := make([]byte, 32*1024)
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("dockerapi: log frame header: %w", err)
		}
		var w io.Writer
		switch head[0] {
		case 0, 1:
			w = stdout
		case 2:
			w = stderr
		default:
			return fmt.Errorf("dockerapi: unknown log stream %d", head[0])
		}
		size := int64(binary.BigEndian.Uint32(head[4:]))
		n, err := io.CopyBuffer(w, io.LimitReader(r, size), buf)
		if err != nil {
			return fmt.Errorf("dockerapi: log frame payload: %w", err)
		}
		if n != size {
			return fmt.Errorf("dockerapi: log frame payload: %d of %d bytes: %w", n, size, io.ErrUnexpectedEOF)
		}
	}
}
