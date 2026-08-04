package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// The attempt directory as the trusted containers see it: one input file in,
// one report file out. The runner reads the report whatever the exit code is.
const (
	attemptDir      = "/attempt"
	stageInputPath  = attemptDir + "/stage.json"
	stageReportPath = attemptDir + "/stage.report.json"

	// contractFileLimit caps what a contract file may be: these are a
	// handful of fields, never a stream.
	contractFileLimit = 1 << 20
)

// runStageCmd is the trusted stage container's entrypoint: read
// /attempt/stage.json, stage under the contract, write the report.
// The forge credential arrives ONLY via GIT_CONFIG_* env (config pkg).
func runStageCmd(stdout, stderr io.Writer) int {
	in, err := readJSONFile[gitops.StageInput](stageInputPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rep, err := gitops.Stage(ctx, attemptDir, in, config.StageEnvFromProcess())
	if err != nil {
		// An unclassified failure here is a hiccup in the trusted container,
		// not a verdict on the run: transient, so the attempt can retry.
		var te gitops.TypedError
		if !errors.As(err, &te) {
			te.Outcome = runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeStagingFailed, Message: err.Error()}
		}
		rep = gitops.StageReport{Outcome: &te.Outcome}
		if writeErr := writeJSONFile(stageReportPath, rep); writeErr != nil {
			fmt.Fprintln(stderr, writeErr)
		}
		fmt.Fprintln(stderr, te.Error())
		return 1
	}
	if err := writeJSONFile(stageReportPath, rep); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintf(stdout, "staged %s (%d files, %d bytes)\n", rep.HeadSHA, rep.Files, rep.Bytes)
	return 0
}

// readJSONFile decodes a contract file strictly: unknown fields are rejected
// (default-deny) and the read is capped.
func readJSONFile[T any](path string) (T, error) {
	var v T
	f, err := os.Open(path)
	if err != nil {
		return v, fmt.Errorf("contract: open %s: %w", path, err)
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, contractFileLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("contract: parse %s: %w", path, err)
	}
	return v, nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("contract: encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("contract: write %s: %w", path, err)
	}
	return nil
}
