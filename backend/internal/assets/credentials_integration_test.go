//go:build integration

package assets_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// authJSON builds a Codex auth.json carrying refresh. size, when
// non-zero, pads the ignored "pad" field so the file is exactly that
// many bytes — the cap tests need to land on the boundary.
func authJSON(t *testing.T, refresh string, size int) []byte {
	t.Helper()
	base := fmt.Sprintf(
		`{"auth_mode":"chatgpt","tokens":{"id_token":"eyJhZG0","access_token":"eyJhY2M","refresh_token":%q,"account_id":"acc_1"},"last_refresh":"2026-08-05T00:00:00Z","pad":""}`,
		refresh)
	if size == 0 {
		return []byte(base)
	}
	if size < len(base) {
		t.Fatalf("authJSON: size %d below minimum %d", size, len(base))
	}
	return []byte(strings.Replace(base, `"pad":""`, `"pad":"`+strings.Repeat("x", size-len(base))+`"`, 1))
}

func credFingerprint(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) *string {
	t.Helper()
	var fp *string
	if err := pool.QueryRow(context.Background(),
		"SELECT fingerprint FROM credentials WHERE id = $1", id).Scan(&fp); err != nil {
		t.Fatal(err)
	}
	return fp
}

func credLastUsed(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) *time.Time {
	t.Helper()
	var at *time.Time
	if err := pool.QueryRow(context.Background(),
		"SELECT last_used_at FROM credentials WHERE id = $1", id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestCreateCredential_SubscriptionKinds(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	// subscription_token is an opaque string under the flat secret cap.
	sub, err := svc.CreateCredential(ctx, admin, "claude-max", assets.CredentialSubscriptionToken,
		[]byte("sk-ant-oat01-averysecretsubscriptiontoken"))
	if err != nil {
		t.Fatal(err)
	}
	if sub.SecretVersion != 1 {
		t.Fatalf("secret_version = %d, want 1 on create", sub.SecretVersion)
	}
	if fp := credFingerprint(t, pool, sub.ID); fp != nil {
		t.Fatalf("fingerprint = %q, want NULL for a non-auth-file kind", *fp)
	}

	// agent_auth_file is fingerprinted on tokens.refresh_token.
	body := authJSON(t, "rt-abc", 0)
	c, err := svc.CreateCredential(ctx, admin, "codex-sub", assets.CredentialAgentAuthFile, body)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("rt-abc"))
	if fp := credFingerprint(t, pool, c.ID); fp == nil || *fp != hex.EncodeToString(sum[:]) {
		t.Fatalf("fingerprint = %v, want hex sha256 of the refresh token", fp)
	}

	// Import is an exclusive custody transfer: a second row holding the
	// same refresh token would run its own lease against one credential.
	err = func() error {
		_, err := svc.CreateCredential(ctx, admin, "codex-sub-2", assets.CredentialAgentAuthFile, body)
		return err
	}()
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("duplicate refresh token: %v, want conflict", err)
	}
	// The name is free, so this must be the fingerprint index reporting,
	// not the name index.
	if !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("conflict = %q, want the custody-transfer message", err)
	}
	// A different refresh token is a different credential.
	if _, err := svc.CreateCredential(ctx, admin, "codex-sub-3", assets.CredentialAgentAuthFile, authJSON(t, "rt-other", 0)); err != nil {
		t.Fatal(err)
	}
	// Two non-fingerprinted kinds coexist: the unique index is partial.
	if _, err := svc.CreateCredential(ctx, admin, "claude-max-2", assets.CredentialSubscriptionToken, []byte("sk-ant-oat01-second")); err != nil {
		t.Fatal(err)
	}
}

func TestCreateCredential_AgentAuthFileCaps(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	// auth.json is a multi-token file: it gets its own 64 KiB bound,
	// well past the flat cap the single-token kinds live under.
	big := authJSON(t, "rt-big", 64<<10)
	if _, err := svc.CreateCredential(ctx, admin, "at-cap", assets.CredentialAgentAuthFile, big); err != nil {
		t.Fatalf("64 KiB auth.json must be accepted: %v", err)
	}
	if _, err := svc.CreateCredential(ctx, admin, "over-cap", assets.CredentialAgentAuthFile,
		authJSON(t, "rt-over", 64<<10+1)); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("64 KiB + 1 auth.json must be invalid")
	}
	if _, err := svc.CreateCredential(ctx, admin, "not-json", assets.CredentialAgentAuthFile,
		[]byte("rt-plain-token")); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("non-JSON auth.json must be invalid")
	}
	if _, err := svc.CreateCredential(ctx, admin, "no-refresh", assets.CredentialAgentAuthFile,
		[]byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"eyJhY2M"}}`)); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("auth.json without tokens.refresh_token must be invalid")
	}
	// The 64 KiB bound belongs to agent_auth_file alone.
	if _, err := svc.CreateCredential(ctx, admin, "fat-sub", assets.CredentialSubscriptionToken,
		bytes.Repeat([]byte("a"), 4097)); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("oversized subscription_token must be invalid")
	}
}

func TestRotateCredentialSecret_BumpsVersionAndFingerprint(t *testing.T) {
	ctx := context.Background()
	svc, pool, fake := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	c, err := svc.CreateCredential(ctx, admin, "codex-sub", assets.CredentialAgentAuthFile, authJSON(t, "rt-1", 0))
	if err != nil {
		t.Fatal(err)
	}
	before := credFingerprint(t, pool, c.ID)

	// created_at/updated_at default to the database clock while updates
	// carry the service clock, so compare the rotation stamp against the
	// clock that wrote it rather than against the create stamp.
	fake.Advance(time.Hour)
	rot, err := svc.RotateCredentialSecret(ctx, admin, c.ID, authJSON(t, "rt-2", 0))
	if err != nil {
		t.Fatal(err)
	}
	if rot.SecretVersion != 2 {
		t.Fatalf("secret_version = %d, want 2 after one rotation", rot.SecretVersion)
	}
	if !rot.UpdatedAt.Equal(fake.Now()) || rot.UpdatedAt.Equal(c.UpdatedAt) {
		t.Fatalf("updated_at = %s, want the rotation moment %s", rot.UpdatedAt, fake.Now())
	}
	after := credFingerprint(t, pool, c.ID)
	if after == nil || before == nil || *after == *before {
		t.Fatalf("fingerprint %v → %v, want a new digest", before, after)
	}

	// Rotating onto a refresh token another row already holds conflicts.
	other, err := svc.CreateCredential(ctx, admin, "codex-sub-2", assets.CredentialAgentAuthFile, authJSON(t, "rt-3", 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RotateCredentialSecret(ctx, admin, other.ID, authJSON(t, "rt-2", 0)); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("rotate onto a held refresh token: %v, want conflict", err)
	}
}

func TestUseCredentialVersioned_ReportsVersionAndStampsUse(t *testing.T) {
	ctx := context.Background()
	svc, pool, fake := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	c, err := svc.CreateCredential(ctx, admin, "codex-sub", assets.CredentialAgentAuthFile, authJSON(t, "rt-1", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := credLastUsed(t, pool, c.ID); got != nil {
		t.Fatalf("last_used_at = %v before any use, want NULL", got)
	}

	plain, version, err := svc.UseCredentialVersioned(ctx, c.ID, "materialize-auth")
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
	if !bytes.Equal(plain, authJSON(t, "rt-1", 0)) {
		t.Fatalf("plaintext = %q, want the stored auth.json", plain)
	}
	if got := credLastUsed(t, pool, c.ID); got == nil || !got.Equal(fake.Now()) {
		t.Fatalf("last_used_at = %v, want the use moment %s", got, fake.Now())
	}

	// The version a use reports is the one whose bytes it handed back:
	// after a rotation, the same call reports the new version.
	if _, err := svc.RotateCredentialSecret(ctx, admin, c.ID, authJSON(t, "rt-2", 0)); err != nil {
		t.Fatal(err)
	}
	plain, version, err = svc.UseCredentialVersioned(ctx, c.ID, "materialize-auth")
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 || !bytes.Equal(plain, authJSON(t, "rt-2", 0)) {
		t.Fatalf("after rotate: version %d, plaintext %q", version, plain)
	}
	if got := auditCount(t, pool, "credential.used"); got != 2 {
		t.Fatalf("credential.used audits = %d, want 2", got)
	}
	if _, _, err := svc.UseCredentialVersioned(ctx, uuid.New(), "gone"); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown credential must be not found")
	}
}

func TestMarkCredentialAuthExpired_RecordsDeathAndAudits(t *testing.T) {
	ctx := context.Background()
	svc, pool, fake := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	c, err := svc.CreateCredential(ctx, admin, "codex-sub", assets.CredentialAgentAuthFile, authJSON(t, "rt-1", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkCredentialAuthExpired(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetCredential(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastAuthExpiredAt == nil || !got.LastAuthExpiredAt.Equal(fake.Now()) {
		t.Fatalf("last_auth_expired_at = %v, want %s", got.LastAuthExpiredAt, fake.Now())
	}
	if n := auditCount(t, pool, "credential.auth_expired"); n != 1 {
		t.Fatalf("credential.auth_expired audits = %d, want 1", n)
	}
	var actorIsNull bool
	if err := pool.QueryRow(ctx,
		"SELECT actor_user_id IS NULL FROM audit_entries WHERE action = 'credential.auth_expired'").Scan(&actorIsNull); err != nil {
		t.Fatal(err)
	}
	if !actorIsNull {
		t.Fatal("credential.auth_expired audit must have a NULL actor_user_id (machine path)")
	}
	if err := svc.MarkCredentialAuthExpired(ctx, uuid.New()); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown credential must be not found")
	}
}

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
