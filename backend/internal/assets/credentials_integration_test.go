//go:build integration

package assets_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestCredentials_SecretIsEncryptedAtRestAndUseIsAudited(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	secret := []byte("ghp_averysecrettoken1234")

	cred, err := svc.CreateCredential(ctx, admin, "github-bot", assets.CredentialForgeToken, secret)
	if err != nil {
		t.Fatal(err)
	}
	if cred.KeyID != "test" {
		t.Fatalf("key_id = %q, want active keyset id", cred.KeyID)
	}

	// The plaintext must not appear anywhere in the row.
	var wrapped, ct []byte
	if err := pool.QueryRow(ctx, "SELECT wrapped_dek, ciphertext FROM credentials WHERE id = $1", cred.ID).Scan(&wrapped, &ct); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, secret) || bytes.Contains(wrapped, secret) {
		t.Fatal("plaintext stored")
	}

	got, err := svc.UseCredential(ctx, cred.ID, "test-clone")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("use = %q, want original secret", got)
	}
	var purpose string
	if err := pool.QueryRow(ctx,
		"SELECT detail->>'purpose' FROM audit_entries WHERE action = 'credential.used'").Scan(&purpose); err != nil {
		t.Fatal(err)
	}
	if purpose != "test-clone" {
		t.Fatalf("audited purpose = %q", purpose)
	}
	var actorIsNull bool
	if err := pool.QueryRow(ctx,
		"SELECT actor_user_id IS NULL FROM audit_entries WHERE action = 'credential.used'").Scan(&actorIsNull); err != nil {
		t.Fatal(err)
	}
	if !actorIsNull {
		t.Fatal("credential.used audit must have a NULL actor_user_id (system-triggered)")
	}
}

func TestCredentials_RotateDeleteAndGuards(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	cred, err := svc.CreateCredential(ctx, admin, "anthropic", assets.CredentialModelKey, []byte("sk-ant-first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCredential(ctx, admin, "anthropic", assets.CredentialModelKey, []byte("x")); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatal("duplicate name must conflict")
	}
	if _, err := svc.CreateCredential(ctx, admin, "big", assets.CredentialModelKey, bytes.Repeat([]byte("a"), 4097)); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("oversized secret must be invalid")
	}
	if _, err := svc.CreateCredential(ctx, admin, "empty", assets.CredentialModelKey, nil); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("empty secret must be invalid")
	}
	if _, err := svc.CreateCredential(ctx, admin, "weird", "ssh_key", []byte("x")); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("unknown kind must be invalid")
	}

	if _, err := svc.RotateCredentialSecret(ctx, admin, cred.ID, []byte("sk-ant-second")); err != nil {
		t.Fatal(err)
	}
	got, err := svc.UseCredential(ctx, cred.ID, "post-rotate")
	if err != nil || string(got) != "sk-ant-second" {
		t.Fatalf("after rotate: %q, %v", got, err)
	}

	// A ciphertext moved onto another row must not decrypt (AAD binding).
	other, err := svc.CreateCredential(ctx, admin, "other", assets.CredentialModelKey, []byte("sk-other"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE credentials SET wrapped_dek = (SELECT wrapped_dek FROM credentials WHERE id = $1), ciphertext = (SELECT ciphertext FROM credentials WHERE id = $1) WHERE id = $2",
		cred.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UseCredential(ctx, other.ID, "swapped"); err == nil {
		t.Fatal("swapped ciphertext must fail to open")
	}

	if err := svc.DeleteCredential(ctx, admin, cred.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UseCredential(ctx, cred.ID, "gone"); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("use after delete must be not found")
	}
	if err := svc.DeleteCredential(ctx, admin, uuid.New()); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown delete must be not found")
	}

	// Only "anthropic" and "other" actually get created: the duplicate
	// name, oversized secret, empty secret, and unknown-kind attempts
	// above are all rejected before the audited commit.
	for action, want := range map[string]int{
		"credential.created": 2, "credential.rotated": 1, "credential.deleted": 1,
	} {
		if got := auditCount(t, pool, action); got != want {
			t.Errorf("%s audits = %d, want %d", action, got, want)
		}
	}
}

func TestDeleteCredential_RefusedWhileReferenced(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	cred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.CreateProject(ctx, admin, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO repos (project_id, forge_kind, slug, credential_id) VALUES ($1, 'github', 'a/b', $2)",
		p.ID, cred.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCredential(ctx, admin, cred.ID); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("class = %v, want conflict while referenced", xerrors.ClassOf(err))
	}
}
