//go:build integration

package assets_test

import (
	"bytes"
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
	// An agent_auth_file carries a non-NULL fingerprint, and rotating it
	// once moves secret_version off the column default — so the "rekey
	// changed nothing" assertions below cannot pass by coincidence.
	three, err := svc.CreateCredential(ctx, admin, "three", assets.CredentialAgentAuthFile, authJSON(t, "rt-1", 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RotateCredentialSecret(ctx, admin, three.ID, authJSON(t, "rt-2", 0)); err != nil {
		t.Fatal(err)
	}
	beforeVersion, beforeFingerprint, beforeUpdated := credentialSecretState(t, pool, three.ID)
	if beforeVersion != 2 || beforeFingerprint == nil {
		t.Fatalf("setup: version %d, fingerprint %v; want 2 and a digest", beforeVersion, beforeFingerprint)
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
	if n != 3 {
		t.Fatalf("rekeyed = %d, want 3", n)
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
	if got := auditCount(t, pool, "credential.rekeyed"); got != 3 {
		t.Fatalf("rekeyed audits = %d, want 3", got)
	}

	// Rekeying re-wraps the plaintext it already holds; it does not
	// supply a new secret. So secret_version, fingerprint, and updated_at
	// ("when the operator last supplied the secret", surfaced as the
	// API's provided_at) must all survive it untouched. Routing rekey
	// back through the rotation query would advance secret_version on
	// every credential without the bytes changing, which would make a
	// version-pinned redaction set look stale when it is not.
	afterVersion, afterFingerprint, afterUpdated := credentialSecretState(t, pool, three.ID)
	if afterVersion != beforeVersion {
		t.Errorf("secret_version %d → %d across a rekey, want unchanged", beforeVersion, afterVersion)
	}
	if afterFingerprint == nil {
		t.Errorf("fingerprint %q → NULL across a rekey, want unchanged", *beforeFingerprint)
	} else if *afterFingerprint != *beforeFingerprint {
		t.Errorf("fingerprint %q → %q across a rekey, want unchanged", *beforeFingerprint, *afterFingerprint)
	}
	if !afterUpdated.Equal(beforeUpdated) {
		t.Errorf("updated_at %s → %s across a rekey, want unchanged", beforeUpdated, afterUpdated)
	}
	// The point of the rekey still happened on that row.
	authPlain, err := rotated.UseCredential(ctx, three.ID, "post-rekey")
	if err != nil || !bytes.Equal(authPlain, authJSON(t, "rt-2", 0)) {
		t.Fatalf("auth file after rekey = %q, %v", authPlain, err)
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
