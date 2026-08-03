//go:build integration

package assets_test

import (
	"context"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

const rotatedKeyset = "next:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzA=,test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestRekeyCredentials_RewrapsUnderActiveKey(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t) // active key id "test"
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	one, err := svc.CreateCredential(ctx, admin, "one", assets.CredentialForgeToken, []byte("secret-one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCredential(ctx, admin, "two", assets.CredentialModelKey, []byte("secret-two")); err != nil {
		t.Fatal(err)
	}

	// New service on the SAME database with a rotated keyset: "next"
	// active, "test" decrypt-only.
	keys, err := secrets.ParseKeyset(rotatedKeyset)
	if err != nil {
		t.Fatal(err)
	}
	fake := clock.NewFake(time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC))
	rotated := assets.NewService(pool, fake, audit.NewRecorder(fake), keys)

	n, err := rotated.RekeyCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rekeyed = %d, want 2", n)
	}
	// Idempotent: second run touches nothing.
	if n, err := rotated.RekeyCredentials(ctx); err != nil || n != 0 {
		t.Fatalf("second rekey = %d, %v; want 0", n, err)
	}

	// Rows now carry the new key id and still decrypt.
	var keyID string
	if err := pool.QueryRow(ctx, "SELECT key_id FROM credentials WHERE id = $1", one.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if keyID != "next" {
		t.Fatalf("key_id = %q, want next", keyID)
	}
	plain, err := rotated.UseCredential(ctx, one.ID, "post-rekey")
	if err != nil || string(plain) != "secret-one" {
		t.Fatalf("use after rekey = %q, %v", plain, err)
	}
	if got := auditCount(t, pool, "credential.rekeyed"); got != 2 {
		t.Fatalf("rekeyed audits = %d, want 2", got)
	}
	var allActorsNull bool
	if err := pool.QueryRow(ctx,
		"SELECT bool_and(actor_user_id IS NULL) FROM audit_entries WHERE action = 'credential.rekeyed'").Scan(&allActorsNull); err != nil {
		t.Fatal(err)
	}
	if !allActorsNull {
		t.Fatal("credential.rekeyed audits must have a NULL actor_user_id (system-triggered)")
	}
}
