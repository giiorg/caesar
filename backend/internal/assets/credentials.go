package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// credentialRow is the metadata shape sqlc returns from the four
// credential queries that project it. sqlc emits a distinct row type per
// query (CreateCredentialRow, GetCredentialRow, ListCredentialsRow,
// UpdateCredentialSecretRow) even though their columns are identical,
// since none matches the full Credential model (which also carries
// wrapped_dek/ciphertext) — so newCredential takes the shared fields
// directly rather than one row struct.
type credentialRow struct {
	id                uuid.UUID
	name, kind, keyID string
	secretVersion     int64
	lastUsedAt        *time.Time
	lastAuthExpiredAt *time.Time
	createdAt         time.Time
	updatedAt         time.Time
}

func newCredential(r credentialRow) Credential {
	return Credential{
		ID: r.id, Name: r.name, Kind: CredentialKind(r.kind), KeyID: r.keyID,
		SecretVersion: r.secretVersion, LastUsedAt: r.lastUsedAt, LastAuthExpiredAt: r.lastAuthExpiredAt,
		CreatedAt: r.createdAt, UpdatedAt: r.updatedAt,
	}
}

// validateSecret enforces the per-kind size and shape rules (spec §5).
// The flat cap fits single tokens; auth.json is a multi-token file with
// its own bound. agent_auth_file must parse: caesar reads pinned paths
// from it (fingerprint here, sensitive fields + identity in the runner)
// and a file that cannot yield them is unusable, better refused at the
// door than at materialization time.
func validateSecret(kind CredentialKind, secret []byte) error {
	limit := maxSecretLen
	if kind == CredentialAgentAuthFile {
		limit = maxAuthFileLen
	}
	if len(secret) == 0 || len(secret) > limit {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "secret", Message: fmt.Sprintf("must be 1-%d bytes", limit),
		})
	}
	if kind == CredentialAgentAuthFile {
		if _, err := authFileFingerprint(secret); err != nil {
			return err
		}
	}
	return nil
}

// authFile is the pinned subset of Codex's auth.json caesar reads.
// Unknown fields are deliberately tolerated — the CLI owns the schema
// (spec §2), and rejecting what we do not understand would break on the
// next CLI release.
type authFile struct {
	Tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	OpenAIAPIKey string `json:"OPENAI_API_KEY"`
}

// authFileFingerprint identifies an auth.json by its refresh token —
// the one field that survives access-token refreshes, so the same
// underlying subscription keeps the same fingerprint across rotations
// that only renew the short-lived tokens.
func authFileFingerprint(secret []byte) (string, error) {
	var f authFile
	if err := json.Unmarshal(secret, &f); err != nil || f.Tokens.RefreshToken == "" {
		return "", xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "secret", Message: "must be a Codex auth.json with tokens.refresh_token",
		})
	}
	sum := sha256.Sum256([]byte(f.Tokens.RefreshToken))
	return hex.EncodeToString(sum[:]), nil
}

// credentialFingerprint is the value stored in the partial unique index:
// set for agent_auth_file, NULL for every other kind.
func credentialFingerprint(kind CredentialKind, secret []byte) (pgtype.Text, error) {
	if kind != CredentialAgentAuthFile {
		return pgtype.Text{}, nil
	}
	fp, err := authFileFingerprint(secret)
	if err != nil {
		return pgtype.Text{}, err
	}
	return pgtype.Text{String: fp, Valid: true}, nil
}

// errDuplicateRefreshToken is what the fingerprint index reports. Two
// rows holding one refresh token would run independent single-flight
// leases against the same upstream credential and refresh each other out
// of validity.
func errDuplicateRefreshToken() error {
	return xerrors.E(xerrors.ClassConflict,
		"an active credential already holds this refresh token — import transfers custody (spec: exclusive custody transfer)")
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
			WithFields(xerrors.FieldError{Field: "kind", Message: "must be one of " + joinKinds(credentialKinds)})
	}
	if err := validateSecret(kind, secret); err != nil {
		return Credential{}, err
	}
	fingerprint, err := credentialFingerprint(kind, secret)
	if err != nil {
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
			Fingerprint: fingerprint,
		})
		if constraint, dup := uniqueViolation(err); dup {
			if constraint == fingerprintIndex {
				return errDuplicateRefreshToken()
			}
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
		c = newCredential(credentialRow{
			id: row.ID, name: row.Name, kind: row.Kind, keyID: row.KeyID,
			secretVersion: row.SecretVersion, lastUsedAt: row.LastUsedAt, lastAuthExpiredAt: row.LastAuthExpiredAt,
			createdAt: row.CreatedAt, updatedAt: row.UpdatedAt,
		})
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
		out = append(out, newCredential(credentialRow{
			id: r.ID, name: r.Name, kind: r.Kind, keyID: r.KeyID,
			secretVersion: r.SecretVersion, lastUsedAt: r.LastUsedAt, lastAuthExpiredAt: r.LastAuthExpiredAt,
			createdAt: r.CreatedAt, updatedAt: r.UpdatedAt,
		}))
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
	return newCredential(credentialRow{
		id: row.ID, name: row.Name, kind: row.Kind, keyID: row.KeyID,
		secretVersion: row.SecretVersion, lastUsedAt: row.LastUsedAt, lastAuthExpiredAt: row.LastAuthExpiredAt,
		createdAt: row.CreatedAt, updatedAt: row.UpdatedAt,
	}), nil
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

// RotateCredentialSecret replaces the stored plaintext. The kind decides
// how the new bytes are validated and fingerprinted, so the current kind
// is read in-tx before the write (kind is immutable, so this is
// race-free). Every rotation advances secret_version: a consumer holding
// version N can tell its bytes are stale without seeing either secret.
func (s *Service) RotateCredentialSecret(ctx context.Context, actor authn.Principal, id uuid.UUID, secret []byte) (Credential, error) {
	kind, err := s.credentialKind(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	if err := validateSecret(kind, secret); err != nil {
		return Credential{}, err
	}
	fingerprint, err := credentialFingerprint(kind, secret)
	if err != nil {
		return Credential{}, err
	}
	env, err := s.keys.Seal(secret, []byte(id.String()))
	if err != nil {
		return Credential{}, fmt.Errorf("assets: seal credential: %w", err)
	}

	var c Credential
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).UpdateCredentialSecret(ctx, store.UpdateCredentialSecretParams{
			ID: id, KeyID: env.KeyID, WrappedDek: env.WrappedDEK, Ciphertext: env.Ciphertext,
			Fingerprint: fingerprint, UpdatedAt: s.clock.Now(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if constraint, dup := uniqueViolation(err); dup && constraint == fingerprintIndex {
			return errDuplicateRefreshToken()
		}
		if err != nil {
			return fmt.Errorf("assets: rotate credential: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "credential.rotated", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"key_id": env.KeyID, "secret_version": row.SecretVersion},
		}); err != nil {
			return err
		}
		c = newCredential(credentialRow{
			id: row.ID, name: row.Name, kind: row.Kind, keyID: row.KeyID,
			secretVersion: row.SecretVersion, lastUsedAt: row.LastUsedAt, lastAuthExpiredAt: row.LastAuthExpiredAt,
			createdAt: row.CreatedAt, updatedAt: row.UpdatedAt,
		})
		return nil
	})
	if err != nil {
		return Credential{}, err
	}
	return c, nil
}

// credentialKind reads a kind on the pool, for callers that need it
// before opening their transaction.
func (s *Service) credentialKind(ctx context.Context, id uuid.UUID) (CredentialKind, error) {
	return getCredentialKind(ctx, store.New(s.pool), id)
}

// MarkCredentialAuthExpired records auth death for the freshness view
// (spec §5). Called by the runner through its port when an attempt maps
// to auth_expired: there is no operator behind it, so the audit carries
// no actor.
func (s *Service) MarkCredentialAuthExpired(ctx context.Context, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.clock.Now()
		n, err := store.New(tx).MarkCredentialAuthExpired(ctx, store.MarkCredentialAuthExpiredParams{
			ID: id, LastAuthExpiredAt: &now,
		})
		if err != nil {
			return fmt.Errorf("assets: mark credential auth expired: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.auth_expired", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
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

// UseCredential decrypts a credential for a control-plane use site whose
// caller does not care which version it got.
func (s *Service) UseCredential(ctx context.Context, id uuid.UUID, purpose string) ([]byte, error) {
	plain, _, err := s.UseCredentialVersioned(ctx, id, purpose)
	return plain, err
}

// UseCredentialVersioned decrypts a credential and reports the
// secret_version the returned bytes belong to. Every call is audited
// (spec §6: every use audited) with the machine purpose; the audit row
// and the last_used_at stamp commit before decryption, so even a failed
// open leaves a trace. Plaintext lives only in the returned slice.
//
// The version is what lets a use site prove completeness later: a
// crash-adopted attempt can only claim its redaction set covers every
// secret the container saw if the version it recorded still matches.
func (s *Service) UseCredentialVersioned(ctx context.Context, id uuid.UUID, purpose string) ([]byte, int64, error) {
	var (
		env     secrets.Envelope
		version int64
	)
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.clock.Now()
		row, err := store.New(tx).UseCredentialRow(ctx, store.UseCredentialRowParams{ID: id, LastUsedAt: &now})
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: get credential row: %w", err)
		}
		env = secrets.Envelope{KeyID: row.KeyID, WrappedDEK: row.WrappedDek, Ciphertext: row.Ciphertext}
		version = row.SecretVersion
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.used", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"purpose": purpose, "secret_version": version},
		})
	})
	if err != nil {
		return nil, 0, err
	}
	plain, err := s.keys.Open(env, []byte(id.String()))
	if err != nil {
		return nil, 0, fmt.Errorf("assets: open credential %s: %w", id, err)
	}
	return plain, version, nil
}
