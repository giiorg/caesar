package assets

import (
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

type Service struct {
	pool  *pgxpool.Pool
	clock clock.Clock
	audit *audit.Recorder
	keys  *secrets.Keyset
}

func NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder, keys *secrets.Keyset) *Service {
	return &Service{pool: pool, clock: c, audit: rec, keys: keys}
}

func actorID(p authn.Principal) uuid.NullUUID {
	return uuid.NullUUID{UUID: p.UserID, Valid: true}
}

// fingerprintIndex is the partial unique index on credentials
// (fingerprint); a violation of it means a different credential already
// holds the same refresh token, not a name collision.
const fingerprintIndex = "credentials_fingerprint_key"

func isUniqueViolation(err error) bool {
	_, dup := uniqueViolation(err)
	return dup
}

// uniqueViolation returns the violated constraint name for unique
// violations, so callers on a table with more than one unique constraint
// can tell which one fired.
func uniqueViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// fkViolation returns the violated constraint name for foreign-key errors,
// so callers can turn "which FK" into the right client-facing error. Our
// schema's cross-aggregate FKs are all ON DELETE RESTRICT, which Postgres
// reports as 23001 (restrict_violation) — 23503 (foreign_key_violation) is
// what NO ACTION constraints raise instead. Accept both.
func fkViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23001" || pgErr.Code == "23503") {
		return pgErr.ConstraintName, true
	}
	return "", false
}
