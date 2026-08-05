package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// runsDefinitionSource adapts workflows.Service to runs.DefinitionSource:
// parse the stored document with the definitions context's own parser,
// then map onto the runs mirror mechanically. Only app may see both
// vocabularies (composition root).
type runsDefinitionSource struct{ svc *workflows.Service }

func (s runsDefinitionSource) Definition(ctx context.Context, workflowID uuid.UUID, version int32) (runs.PinnedDefinition, error) {
	v, err := s.svc.GetVersion(ctx, workflowID, version)
	if err != nil {
		return runs.PinnedDefinition{}, err
	}
	return pinnedFromVersion(v)
}

func (s runsDefinitionSource) DefinitionByVersionID(ctx context.Context, versionID uuid.UUID) (runs.PinnedDefinition, error) {
	v, err := s.svc.GetVersionByID(ctx, versionID)
	if err != nil {
		return runs.PinnedDefinition{}, err
	}
	return pinnedFromVersion(v)
}

func pinnedFromVersion(v workflows.Version) (runs.PinnedDefinition, error) {
	doc, err := workflows.ParseDocument(v.Document)
	if err != nil {
		return runs.PinnedDefinition{}, fmt.Errorf("app: stored definition %s unparseable: %w", v.ID, err)
	}
	mirror := runs.DefinitionDoc{}
	for _, n := range doc.Nodes {
		mirror.Nodes = append(mirror.Nodes, runs.DefNode{
			ID: n.ID, Kind: string(n.Type), Harness: n.Harness, Instructions: n.Instructions,
		})
	}
	for _, e := range doc.Edges {
		mirror.Edges = append(mirror.Edges, runs.DefEdge{From: e.From, To: e.To, Artifact: string(e.Artifact)})
	}
	return runs.PinnedDefinition{
		VersionID: v.ID, WorkflowID: v.WorkflowID, Version: v.Version,
		ContentHash: v.ContentHash, Doc: mirror,
	}, nil
}

// runsAssetSource adapts assets.Service to RepoSource + HarnessSource.
type runsAssetSource struct{ svc *assets.Service }

func (s runsAssetSource) Repo(ctx context.Context, id uuid.UUID) (runs.RepoRef, error) {
	r, err := s.svc.GetRepo(ctx, id)
	if err != nil {
		return runs.RepoRef{}, err
	}
	return runs.RepoRef{
		ID: r.ID, ProjectID: r.ProjectID, CredentialID: r.CredentialID,
		Forge: string(r.Forge), Slug: r.Slug, DefaultBranch: r.DefaultBranch,
	}, nil
}

// HarnessByName resolves the harness and, with it, the kind of credential it
// authenticates with: the spine pins both into the attempt's binding, so the
// runner never re-reads the mutable harness row to learn either.
func (s runsAssetSource) HarnessByName(ctx context.Context, name string) (runs.HarnessRef, error) {
	h, err := s.svc.GetHarnessByName(ctx, name)
	if err != nil {
		return runs.HarnessRef{}, err
	}
	c, err := s.svc.GetCredential(ctx, h.CredentialID)
	if err != nil {
		return runs.HarnessRef{}, err
	}
	return runs.HarnessRef{
		ID: h.ID, Kind: string(h.Kind),
		CredentialID: h.CredentialID, CredentialKind: string(c.Kind),
	}, nil
}

// unavailableForge refuses task intake honestly until Plan 08's forge
// adapters exist: no run can pin a base SHA, so no task can be created
// through production wiring yet (stubs never fake success).
type unavailableForge struct{}

func (unavailableForge) ResolveHead(context.Context, runs.RepoRef) (string, error) {
	return "", xerrors.Wrap(xerrors.ClassInvalid, "forge adapters arrive in Plan 08; task intake is not yet available in production wiring", xerrors.ErrNotImplemented)
}
