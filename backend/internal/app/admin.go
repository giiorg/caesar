package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
)

const adminUsage = `usage: caesar admin bootstrap -email <email> [-config path]

Reads the password from stdin (pipe or interactive) and creates the first
admin through the normal domain service. Refuses once any admin exists.
`

func runAdminCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "bootstrap" {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	fs := flag.NewFlagSet("admin bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "caesar.json", "path to config file")
	email := fs.String("email", "", "email of the first admin")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *email == "" {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "read password from stdin:", err)
		return 1
	}
	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		fmt.Fprint(stderr, "empty password on stdin\n\n"+adminUsage)
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
	if err := pg.Migrate(ctx, pool, pg.Migrations()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	svc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL:     cfg.Auth.SessionIdleTTL.Std(),
		SessionAbsoluteTTL: cfg.Auth.SessionAbsoluteTTL.Std(),
	})
	user, err := svc.Bootstrap(ctx, *email, password)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "bootstrap: created admin %s (%s)\n", user.Email, user.ID)
	return 0
}
