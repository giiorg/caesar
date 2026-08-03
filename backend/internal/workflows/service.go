package workflows

import (
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
)

type Service struct {
	pool  *pgxpool.Pool
	clock clock.Clock
	audit *audit.Recorder
}

func NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder) *Service {
	return &Service{pool: pool, clock: c, audit: rec}
}

func actorID(p authn.Principal) uuid.NullUUID {
	return uuid.NullUUID{UUID: p.UserID, Valid: true}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isFKViolation reports a blocked delete. The task -> workflow_version FK
// is ON DELETE RESTRICT, which Postgres raises as 23001
// (restrict_violation); 23503 (foreign_key_violation) is what NO ACTION
// constraints raise instead. Accept both.
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23001" || pgErr.Code == "23503")
}
