package fake

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// Forge resolves heads from a scripted map — deterministic, no network.
type Forge struct {
	Heads map[uuid.UUID]string
}

func (f Forge) ResolveHead(_ context.Context, repo runs.RepoRef) (string, error) {
	sha, ok := f.Heads[repo.ID]
	if !ok {
		return "", fmt.Errorf("fake forge: no head for repo %s", repo.ID)
	}
	return sha, nil
}
