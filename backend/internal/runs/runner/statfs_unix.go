//go:build linux || darwin

package runner

import (
	"fmt"
	"syscall"
)

// diskFreeBytes reports the space available to this (unprivileged) process
// on the filesystem holding path — Bavail, not Bfree, because the
// root-reserved blocks are not ours to spend.
func diskFreeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return widen(st.Bavail) * widen(st.Bsize), nil
}

// widen normalizes the statfs field types, which differ by platform (linux
// spells the block size int64, darwin uint32), to one arithmetic type.
func widen[T int32 | int64 | uint32 | uint64](v T) int64 { return int64(v) }
