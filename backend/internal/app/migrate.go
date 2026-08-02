package app

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
)

func runMigrateCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "caesar.json", "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	direction := fs.Arg(0)
	if direction == "" {
		direction = "status"
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(stderr, "usage: migrate [up|down|status] [-config path]: flags must come before the direction, got trailing args %v\n", fs.Args()[1:])
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	pool, err := pg.New(ctx, cfg.Database.URL)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer pool.Close()

	switch direction {
	case "up":
		err = pg.Migrate(ctx, pool, pg.Migrations())
	case "down":
		err = pg.MigrateDown(ctx, pool, pg.Migrations())
	case "status":
		err = pg.MigrateStatus(ctx, pool, pg.Migrations(), stdout)
	default:
		fmt.Fprintf(stderr, "unknown migrate direction %q (want up|down|status)\n", direction)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "migrate %s: ok\n", direction)
	return 0
}
