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
  serve         start the control plane (default)
  migrate       run database migrations (up|down|status)
  admin         administrative commands (bootstrap, rekey)
  stage         clone and verify an attempt workspace (runs inside the stage container)
  extract       compute the canonical patch for an attempt (runs inside the extract container)
  egress-proxy  broker and tunnel an attempt's only route off its network (runs inside the gateway container)
  version       print build information
`

// Main dispatches subcommands. It returns the process exit code; main() is
// the only caller of os.Exit.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	case "migrate":
		return runMigrateCmd(args, stdout, stderr)
	case "serve":
		return runServeCmd(args, stdout, stderr)
	case "admin":
		return runAdminCmd(args, stdin, stdout, stderr)
	case "stage":
		return runStageCmd(stdout, stderr)
	case "extract":
		return runExtractCmd(stdout, stderr)
	case "egress-proxy":
		return runEgressProxyCmd(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}
