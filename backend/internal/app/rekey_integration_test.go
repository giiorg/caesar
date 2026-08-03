//go:build integration

package app_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/app"
	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func TestAdminRekey_EndToEnd(t *testing.T) {
	ctx := context.Background()
	url := testdb.NewURL(t)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Seed one credential under the old key.
	oldKeys, err := secrets.ParseKeyset("old:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	svc := assets.NewService(pool, fake, audit.NewRecorder(fake), oldKeys)
	actor := authn.Principal{Role: authn.RoleAdmin}
	if err := pool.QueryRow(ctx, "INSERT INTO users (email, password_hash, role) VALUES ('root@example.com', 'x', 'admin') RETURNING id").Scan(&actor.UserID); err != nil {
		t.Fatal(err)
	}
	cred, err := svc.CreateCredential(ctx, actor, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}

	// Rotated keyset via config file: "new" active, "old" decrypt-only.
	cfgPath := filepath.Join(t.TempDir(), "caesar.json")
	cfg := fmt.Sprintf(`{"database": {"url": %q}, "security": {"master_key": "new:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzA=,old:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}`, url)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DATABASE_URL", "")
	t.Setenv("CAESAR_MASTER_KEY", "")

	var out, errOut bytes.Buffer
	code := app.Main([]string{"admin", "rekey", "-config", cfgPath}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "1 credential(s) re-wrapped") {
		t.Fatalf("stdout = %q", out.String())
	}
	var keyID string
	if err := pool.QueryRow(ctx, "SELECT key_id FROM credentials WHERE id = $1", cred.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if keyID != "new" {
		t.Fatalf("key_id = %q, want new", keyID)
	}
}
