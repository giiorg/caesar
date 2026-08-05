package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/runs/egress"
)

// runEgressProxyCmd is the gateway container's entrypoint: one attempt's
// broker and CONNECT proxy, configured entirely by CAESAR_EGRESS_CONFIG and
// logging JSON to stdout, where the runner reads the usage summary back.
func runEgressProxyCmd(stdout, stderr io.Writer) int {
	raw, err := config.EgressProxyEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := egress.LoadConfig(raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	log := slog.New(slog.NewJSONHandler(stdout, nil))
	gateway, err := egress.NewGateway(cfg, log)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := gateway.Run(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
