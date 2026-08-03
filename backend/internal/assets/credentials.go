package assets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// newCredential builds the metadata-only view from a store row's fields.
// sqlc emits a distinct row type per query (CreateCredentialRow,
// GetCredentialRow, ListCredentialsRow, UpdateCredentialSecretRow) even
// though their columns are identical, since none matches the full
// Credential model (which also carries wrapped_dek/ciphertext) — so this
// takes the six shared fields directly rather than one row struct.
func newCredential(id uuid.UUID, name, kind, keyID string, createdAt, updatedAt time.Time) Credential {
	return Credential{ID: id, Name: name, Kind: CredentialKind(kind), KeyID: keyID, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

func validateSecret(secret []byte) error {
	if len(secret) == 0 || len(secret) > maxSecretLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "secret", Message: fmt.Sprintf("must be 1-%d bytes", maxSecretLen),
		})
	}
	return nil
}

// CreateCredential seals secret under the active master key. The id is
// generated app-side: it is the AAD binding ciphertext to row.
func (s *Service) CreateCredential(ctx context.Context, actor authn.Principal, name string, kind CredentialKind, secret []byte) (Credential, error) {
	name, err := validateName("name", name)
	if err != nil {
		return Credential{}, err
	}
	if !kind.Valid() {
		return Credential{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "kind", Message: "must be forge_token or model_key"})
	}
	if err := validateSecret(secret); err != nil {
		return Credential{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Credential{}, fmt.Errorf("assets: credential id: %w", err)
	}
	env, err := s.keys.Seal(secret, []byte(id.String()))
	if err != nil {
		return Credential{}, fmt.Errorf("assets: seal credential: %w", err)
	}

	var c Credential
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).CreateCredential(ctx, store.CreateCredentialParams{
			ID: id, Name: name, Kind: string(kind),
			KeyID: env.KeyID, WrappedDek: env.WrappedDEK, Ciphertext: env.Ciphertext,
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "credential name already taken")
		}
		if err != nil {
			return fmt.Errorf("assets: create credential: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "credential.created", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name, "kind": string(kind), "key_id": env.KeyID},
		}); err != nil {
			return err
		}
		c = newCredential(row.ID, row.Name, row.Kind, row.KeyID, row.CreatedAt, row.UpdatedAt)
		return nil
	})
	if err != nil {
		return Credential{}, err
	}
	return c, nil
}

func (s *Service) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := store.New(s.pool).ListCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("assets: list credentials: %w", err)
	}
	out := make([]Credential, 0, len(rows))
	for _, r := range rows {
		out = append(out, newCredential(r.ID, r.Name, r.Kind, r.KeyID, r.CreatedAt, r.UpdatedAt))
	}
	return out, nil
}

func (s *Service) GetCredential(ctx context.Context, id uuid.UUID) (Credential, error) {
	row, err := store.New(s.pool).GetCredential(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, xerrors.E(xerrors.ClassNotFound, "no such credential")
	}
	if err != nil {
		return Credential{}, fmt.Errorf("assets: get credential: %w", err)
	}
	return newCredential(row.ID, row.Name, row.Kind, row.KeyID, row.CreatedAt, row.UpdatedAt), nil
}

// getCredentialKind reads a credential's kind inside a caller's tx —
// repos/harnesses validate referenced kinds with it. Kind never changes
// after creation, so this is race-free.
func getCredentialKind(ctx context.Context, q *store.Queries, id uuid.UUID) (CredentialKind, error) {
	row, err := q.GetCredential(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", xerrors.E(xerrors.ClassNotFound, "no such credential")
	}
	if err != nil {
		return "", fmt.Errorf("assets: get credential: %w", err)
	}
	return CredentialKind(row.Kind), nil
}

func (s *Service) RotateCredentialSecret(ctx context.Context, actor authn.Principal, id uuid.UUID, secret []byte) (Credential, error) {
	if err := validateSecret(secret); err != nil {
		return Credential{}, err
	}
	env, err := s.keys.Seal(secret, []byte(id.String()))
	if err != nil {
		return Credential{}, fmt.Errorf("assets: seal credential: %w", err)
	}

	var c Credential
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).UpdateCredentialSecret(ctx, store.UpdateCredentialSecretParams{
			ID: id, KeyID: env.KeyID, WrappedDek: env.WrappedDEK, Ciphertext: env.Ciphertext, UpdatedAt: s.clock.Now(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: rotate credential: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "credential.rotated", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"key_id": env.KeyID},
		}); err != nil {
			return err
		}
		c = newCredential(row.ID, row.Name, row.Kind, row.KeyID, row.CreatedAt, row.UpdatedAt)
		return nil
	})
	if err != nil {
		return Credential{}, err
	}
	return c, nil
}

func (s *Service) DeleteCredential(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteCredential(ctx, id)
		if _, fk := fkViolation(err); fk {
			return xerrors.E(xerrors.ClassConflict, "credential is referenced by a repo or harness")
		}
		if err != nil {
			return fmt.Errorf("assets: delete credential: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "credential.deleted", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}

// UseCredential decrypts a credential for a control-plane use site. Every
// call is audited (spec §6: every use audited) with the machine purpose;
// the audit row commits before decryption, so even a failed open leaves a
// trace. Plaintext lives only in the returned slice.
func (s *Service) UseCredential(ctx context.Context, id uuid.UUID, purpose string) ([]byte, error) {
	var env secrets.Envelope
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).GetCredentialRow(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: get credential row: %w", err)
		}
		env = secrets.Envelope{KeyID: row.KeyID, WrappedDEK: row.WrappedDek, Ciphertext: row.Ciphertext}
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.used", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"purpose": purpose},
		})
	})
	if err != nil {
		return nil, err
	}
	plain, err := s.keys.Open(env, []byte(id.String()))
	if err != nil {
		return nil, fmt.Errorf("assets: open credential %s: %w", id, err)
	}
	return plain, nil
}
