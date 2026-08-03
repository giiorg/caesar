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

func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
