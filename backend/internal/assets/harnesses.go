package assets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// harnessCredentialKinds is how each harness kind may authenticate: a
// claude-code container takes a Claude subscription token or an
// Anthropic model key, a codex container takes a Codex auth.json or an
// OpenAI model key. A pairing outside this table produces a container
// that cannot authenticate at all, so registration refuses it.
var harnessCredentialKinds = map[HarnessKind][]CredentialKind{
	HarnessClaudeCode: {CredentialSubscriptionToken, CredentialModelKey},
	HarnessCodex:      {CredentialAgentAuthFile, CredentialModelKey},
}

// NewHarness is the registration payload; nil Defaults means {}.
type NewHarness struct {
	Name             string
	Kind             HarnessKind
	ImageRef         string
	ProviderEndpoint string
	CredentialID     uuid.UUID
	Defaults         map[string]any
}

// HarnessUpdate is a partial update; nil fields are left unchanged.
// Kind is immutable — register a new harness instead.
type HarnessUpdate struct {
	Name             *string
	ImageRef         *string
	ProviderEndpoint *string
	CredentialID     *uuid.UUID
	Defaults         *map[string]any
}

func harnessFromRow(r store.Harness) (Harness, error) {
	defaults := map[string]any{}
	if len(r.Defaults) > 0 {
		if err := json.Unmarshal(r.Defaults, &defaults); err != nil {
			return Harness{}, fmt.Errorf("assets: harness defaults: %w", err)
		}
	}
	return Harness{
		ID: r.ID, Name: r.Name, Kind: HarnessKind(r.Kind), ImageRef: r.ImageRef,
		ProviderEndpoint: r.ProviderEndpoint, CredentialID: r.CredentialID, Defaults: defaults,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

func validateImageRef(ref string) error {
	if ref == "" || len(ref) > maxImageRefLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "image_ref", Message: fmt.Sprintf("must be 1-%d characters", maxImageRefLen),
		})
	}
	return nil
}

// validateEndpoint pins provider endpoints to https; plain http passes
// only for localhost (dev). The egress gateway inherits this URL later.
func validateEndpoint(endpoint string) error {
	invalid := xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
		Field: "provider_endpoint", Message: "must be an https URL (http allowed for localhost only)",
	})
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return invalid
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" {
			return nil
		}
	}
	return invalid
}

func marshalDefaults(d map[string]any) ([]byte, error) {
	if d == nil {
		d = map[string]any{}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, xerrors.Wrap(xerrors.ClassInvalid, "defaults must be a JSON object", err)
	}
	if len(raw) > maxDefaultsLen {
		return nil, xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "defaults", Message: fmt.Sprintf("must serialize to at most %d bytes", maxDefaultsLen),
		})
	}
	return raw, nil
}

func (s *Service) RegisterHarness(ctx context.Context, actor authn.Principal, in NewHarness) (Harness, error) {
	name, err := validateName("name", in.Name)
	if err != nil {
		return Harness{}, err
	}
	if !in.Kind.Valid() {
		return Harness{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "kind", Message: "must be claude-code or codex"})
	}
	if err := validateImageRef(in.ImageRef); err != nil {
		return Harness{}, err
	}
	if err := validateEndpoint(in.ProviderEndpoint); err != nil {
		return Harness{}, err
	}
	raw, err := marshalDefaults(in.Defaults)
	if err != nil {
		return Harness{}, err
	}

	var h Harness
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := requireCredentialKindIn(ctx, q, in.CredentialID, harnessCredentialKinds[in.Kind]...); err != nil {
			return err
		}
		row, err := q.RegisterHarness(ctx, store.RegisterHarnessParams{
			Name: name, Kind: string(in.Kind), ImageRef: in.ImageRef,
			ProviderEndpoint: in.ProviderEndpoint, CredentialID: in.CredentialID, Defaults: raw,
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "harness name already taken")
		}
		if _, fk := fkViolation(err); fk {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: register harness: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "harness.registered", SubjectKind: "harness", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name, "kind": string(in.Kind), "image_ref": in.ImageRef},
		}); err != nil {
			return err
		}
		h, err = harnessFromRow(row)
		return err
	})
	if err != nil {
		return Harness{}, err
	}
	return h, nil
}

func (s *Service) ListHarnesses(ctx context.Context) ([]Harness, error) {
	rows, err := store.New(s.pool).ListHarnesses(ctx)
	if err != nil {
		return nil, fmt.Errorf("assets: list harnesses: %w", err)
	}
	out := make([]Harness, 0, len(rows))
	for _, r := range rows {
		h, err := harnessFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

func (s *Service) GetHarness(ctx context.Context, id uuid.UUID) (Harness, error) {
	row, err := store.New(s.pool).GetHarness(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Harness{}, xerrors.E(xerrors.ClassNotFound, "no such harness")
	}
	if err != nil {
		return Harness{}, fmt.Errorf("assets: get harness: %w", err)
	}
	return harnessFromRow(row)
}

// GetHarnessByName resolves a definition's harness reference — the
// execution spine's intake path (wired through app's adapters).
func (s *Service) GetHarnessByName(ctx context.Context, name string) (Harness, error) {
	row, err := store.New(s.pool).GetHarnessByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Harness{}, xerrors.E(xerrors.ClassNotFound, "no such harness")
	}
	if err != nil {
		return Harness{}, fmt.Errorf("assets: get harness by name: %w", err)
	}
	return harnessFromRow(row)
}

func (s *Service) UpdateHarness(ctx context.Context, actor authn.Principal, id uuid.UUID, upd HarnessUpdate) (Harness, error) {
	var h Harness
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		current, err := q.GetHarness(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such harness")
		}
		if err != nil {
			return fmt.Errorf("assets: get harness: %w", err)
		}
		name, imageRef, endpoint, credID, raw := current.Name, current.ImageRef, current.ProviderEndpoint, current.CredentialID, current.Defaults
		if upd.Name != nil {
			if name, err = validateName("name", *upd.Name); err != nil {
				return err
			}
		}
		if upd.ImageRef != nil {
			if err := validateImageRef(*upd.ImageRef); err != nil {
				return err
			}
			imageRef = *upd.ImageRef
		}
		if upd.ProviderEndpoint != nil {
			if err := validateEndpoint(*upd.ProviderEndpoint); err != nil {
				return err
			}
			endpoint = *upd.ProviderEndpoint
		}
		if upd.CredentialID != nil {
			// Kind is immutable, so the row's own kind picks the set.
			if err := requireCredentialKindIn(ctx, q, *upd.CredentialID, harnessCredentialKinds[HarnessKind(current.Kind)]...); err != nil {
				return err
			}
			credID = *upd.CredentialID
		}
		if upd.Defaults != nil {
			if raw, err = marshalDefaults(*upd.Defaults); err != nil {
				return err
			}
		}
		row, err := q.UpdateHarness(ctx, store.UpdateHarnessParams{
			ID: id, Name: name, ImageRef: imageRef, ProviderEndpoint: endpoint,
			CredentialID: credID, Defaults: raw, UpdatedAt: s.clock.Now(),
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "harness name already taken")
		}
		if _, fk := fkViolation(err); fk {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: update harness: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "harness.updated", SubjectKind: "harness", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		h, err = harnessFromRow(row)
		return err
	})
	if err != nil {
		return Harness{}, err
	}
	return h, nil
}

func (s *Service) DeleteHarness(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteHarness(ctx, id)
		if err != nil {
			return fmt.Errorf("assets: delete harness: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such harness")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "harness.deleted", SubjectKind: "harness", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}
