package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// bindingEngine is the smallest daemon an attempt can die against: the image
// resolves, and the very next step — bringing up the egress network — fails.
// That is one step past the credential open, which is all this test watches.
type bindingEngine struct{ fakeEngine }

func (*bindingEngine) NetworkInspect(context.Context, string) (dockerapi.NetworkInspect, error) {
	return dockerapi.NetworkInspect{}, errors.New("daemon unreachable")
}

func (*bindingEngine) ContainerRemove(context.Context, string, bool) error { return nil }

func (*bindingEngine) NetworkRemove(context.Context, string) error { return nil }

// harnessRegistry is the mutable harness row the resolver reads. rotated is
// the credential a re-resolution would find today — the value the runner must
// never reach for, and the reason the resolver cannot hand one over at all.
type harnessRegistry struct {
	spec    HarnessSpec
	rotated uuid.UUID
}

func (h *harnessRegistry) HarnessSpec(context.Context, uuid.UUID) (HarnessSpec, error) {
	return h.spec, nil
}

type recordingOpener struct{ opened []uuid.UUID }

// openedSecretVersion is the version this opener reports for everything it
// hands over. It is deliberately not 1: a manifest carrying it proves the
// version came from the open rather than from a default nobody set.
const openedSecretVersion = 3

func (o *recordingOpener) OpenCredential(_ context.Context, id uuid.UUID, _ string) ([]byte, int64, error) {
	o.opened = append(o.opened, id)
	return []byte("provider-key"), openedSecretVersion, nil
}

type stubGit struct{}

func (stubGit) CloneSpec(context.Context, runs.RepoRef) (CloneSpec, error) {
	return CloneSpec{URL: "https://example.invalid/repo.git"}, nil
}

// The binding race (spec §6): the harness row is repointed at another
// credential between the moment the request pinned one and the moment the
// attempt runs. The attempt opens what it bound — a registry edit mid-flight
// reaches nothing already in flight.
func TestExecute_OpensTheBoundCredentialNotTheHarnessRowsCurrentOne(t *testing.T) {
	bound, rotated := uuid.New(), uuid.New()
	registry := &harnessRegistry{spec: HarnessSpec{Kind: "claude-code", ImageRef: "ghcr.io/x/claude@sha256:beef"}}
	opener := &recordingOpener{}

	eng := &bindingEngine{fakeEngine{inspect: func(string) (dockerapi.ImageInspect, error) {
		return dockerapi.ImageInspect{RepoDigests: []string{"ghcr.io/x/claude@sha256:beef"}}, nil
	}}}
	r, err := New(eng, registry, opener, nil, stubGit{}, nil, Options{
		DataRoot: t.TempDir(), CaesarImage: "caesar:dev", GitImage: "caesar-git:dev",
	}, discardLog())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := runs.ExecutionRequest{
		Ref:        testRef(),
		Kind:       runs.KindImplement,
		HarnessID:  uuid.New(),
		Credential: runs.CredentialBinding{ID: bound, Kind: "model_key"},
		Repo:       runs.RepoRef{Forge: "github", Slug: "giiorg/demo"},
		BaseSHA:    "abc",
	}
	registry.rotated = rotated // the row is edited after the request was built

	if _, err := r.Execute(t.Context(), req); err == nil {
		t.Fatal("want the attempt to die at the network step, past the credential open")
	}
	if len(opener.opened) != 1 {
		t.Fatalf("opened = %v, want exactly the bound credential once", opener.opened)
	}
	if opener.opened[0] != bound {
		t.Errorf("opened %s, want the bound %s (the row now says %s)", opener.opened[0], bound, rotated)
	}
}
