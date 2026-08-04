//go:build integration

package assets_test

import (
	"context"
	"testing"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestGetHarnessByName_RoundTripsOrNotFound(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	modelCred, err := svc.CreateCredential(ctx, admin, "ant", assets.CredentialModelKey, []byte("sk"))
	if err != nil {
		t.Fatal(err)
	}
	made, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "cc", Kind: assets.HarnessClaudeCode,
		ImageRef:         "ghcr.io/giiorg/caesar-harness-claude@sha256:abc",
		ProviderEndpoint: "https://api.anthropic.com",
		CredentialID:     modelCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetHarnessByName(ctx, "cc")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != made.ID || got.Name != "cc" || got.Kind != assets.HarnessClaudeCode || got.CredentialID != modelCred.ID {
		t.Fatalf("harness = %+v, want the registered one", got)
	}

	if _, err := svc.GetHarnessByName(ctx, "nope"); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("unknown name: %v, want not_found", err)
	}
}

func TestHarnesses_RegistryCRUDWithGuards(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	modelCred, err := svc.CreateCredential(ctx, admin, "ant", assets.CredentialModelKey, []byte("sk"))
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}

	h, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "claude-main", Kind: assets.HarnessClaudeCode,
		ImageRef:         "ghcr.io/giiorg/caesar-harness-claude@sha256:abc",
		ProviderEndpoint: "https://api.anthropic.com",
		CredentialID:     modelCred.ID,
		Defaults:         map[string]any{"model": "claude-sonnet-5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Defaults["model"] != "claude-sonnet-5" {
		t.Fatalf("defaults roundtrip = %+v", h.Defaults)
	}

	// Guards.
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "claude-main", Kind: assets.HarnessClaudeCode, ImageRef: "x", ProviderEndpoint: "https://a.example", CredentialID: modelCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatal("duplicate name must conflict")
	}
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "bad-kind", Kind: "aider", ImageRef: "x", ProviderEndpoint: "https://a.example", CredentialID: modelCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("unknown kind must be invalid")
	}
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "http-endpoint", Kind: assets.HarnessCodex, ImageRef: "x", ProviderEndpoint: "http://api.openai.com", CredentialID: modelCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("plain http on a non-localhost endpoint must be invalid")
	}
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "dev-local", Kind: assets.HarnessCodex, ImageRef: "x", ProviderEndpoint: "http://localhost:4000", CredentialID: modelCred.ID,
	}); err != nil {
		t.Fatalf("http localhost must be allowed for dev: %v", err)
	}
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "wrong-cred", Kind: assets.HarnessCodex, ImageRef: "x", ProviderEndpoint: "https://a.example", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("forge_token credential on a harness must be invalid")
	}

	ref := "ghcr.io/giiorg/caesar-harness-claude@sha256:def"
	upd, err := svc.UpdateHarness(ctx, admin, h.ID, assets.HarnessUpdate{ImageRef: &ref})
	if err != nil || upd.ImageRef != ref || upd.Name != "claude-main" {
		t.Fatalf("update = %+v, %v", upd, err)
	}

	all, err := svc.ListHarnesses(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %d, %v", len(all), err)
	}
	if _, err := svc.GetHarness(ctx, h.ID); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteHarness(ctx, admin, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteHarness(ctx, admin, h.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("second delete must be not found")
	}

	for action, want := range map[string]int{"harness.registered": 2, "harness.updated": 1, "harness.deleted": 1} {
		if got := auditCount(t, pool, action); got != want {
			t.Errorf("%s audits = %d, want %d", action, got, want)
		}
	}
}
