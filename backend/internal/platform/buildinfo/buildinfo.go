// Package buildinfo carries the version identity stamped into the binary at
// build time via -ldflags (see the backend:build task).
package buildinfo

import "fmt"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func String() string {
	return fmt.Sprintf("caesar %s (commit %s, built %s)", Version, Commit, Date)
}
