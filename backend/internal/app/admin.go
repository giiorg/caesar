package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

const adminUsage = `usage: caesar admin <bootstrap|rekey>

  bootstrap -email <email> [-config path]   create the first admin
      (password from stdin; refuses once any admin exists)
  rekey [-config path]                      re-wrap credentials under the
      active master key (first CAESAR_MASTER_KEY entry)
`

func runAdminCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	switch args[0] {
	case "bootstrap":
		return runAdminBootstrap(args[1:], stdin, stdout, stderr)
	case "rekey":
		return runAdminRekey(args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
}

func runAdminBootstrap(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "caesar.json", "path to config file")
	email := fs.String("email", "", "email of the first admin")
	if err := fs.Parse(args); err != nil {
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

func runAdminRekey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin rekey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "caesar.json", "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if cfg.Security.MasterKey == "" {
		fmt.Fprintln(stderr, "rekey: security.master_key (or CAESAR_MASTER_KEY) is required")
		return 1
	}
	keys, err := secrets.ParseKeyset(cfg.Security.MasterKey)
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
	svc := assets.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), keys)
	n, err := svc.RekeyCredentials(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "rekey: %d credential(s) re-wrapped under key %q\n", n, keys.ActiveKeyID())
	return 0
}
