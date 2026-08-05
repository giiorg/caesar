package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

const (
	extractInputPath  = attemptDir + "/" + gitops.ExtractInputFile
	extractReportPath = attemptDir + "/" + gitops.ExtractReportFile
)

// runExtractCmd is the trusted extraction container's entrypoint: read
// /attempt/extract.json, compute the canonical patch against the trusted
// mirror, write the report. It runs with no network and never executes git
// against the agent's workspace .git.
func runExtractCmd(stdout, stderr io.Writer) int {
	in, err := readJSONFile[gitops.ExtractInput](extractInputPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rep, err := gitops.Extract(ctx, attemptDir, in)
	if err != nil {
		// Extraction is deterministic in its inputs: an unclassified failure
		// here would fail the same way on a retry, so it is permanent.
		var te gitops.TypedError
		if !errors.As(err, &te) {
			te.Outcome = runs.Outcome{Class: runs.OutcomePermanent, Code: runs.CodeExtractionFailed, Message: err.Error()}
		}
		rep = gitops.ExtractReport{Outcome: &te.Outcome}
		if writeErr := writeJSONFile(extractReportPath, rep); writeErr != nil {
			fmt.Fprintln(stderr, writeErr)
		}
		fmt.Fprintln(stderr, te.Error())
		return 1
	}
	if err := writeJSONFile(extractReportPath, rep); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintf(stdout, "extracted %s (changed=%v, %d patch bytes)\n", rep.TreeHash, rep.Changed, len(rep.Diff))
	return 0
}
