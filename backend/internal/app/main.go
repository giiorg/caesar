// Package app is the composition root: it wires platform packages into the
// runnable caesar binary. cmd/caesar delegates here and nothing else.
package app

import (
	"fmt"
	"io"

	"github.com/giiorg/caesar/backend/internal/platform/buildinfo"
)

const usage = `caesar — deterministic orchestrator of agentic workflows

commands:
  serve     start the control plane (default)
  migrate   run database migrations (up|down|status)
  version   print build information
`

// Main dispatches subcommands. It returns the process exit code; main() is
// the only caller of os.Exit.
func Main(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}
