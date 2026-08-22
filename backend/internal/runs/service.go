package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// Params is the injected policy source until Plan 09's engine: app
// wires DefaultSnapshot(); tests wire short budgets.
//
// Labels is the run READ path's label port. It is here rather than in
// NewService's positional list because every existing call site would otherwise
// have to be edited to pass a nil, and a nil in a positional argument list is
// the kind of wiring omission that reads as intentional. A nil one is refused
// loudly where it is needed (see labelsFor) and never degraded to ids.
type Params struct {
	Snapshot PolicySnapshot
	Labels   RunLabelSource
}

type Service struct {
	pool      *pgxpool.Pool
	clock     clock.Clock
	audit     *audit.Recorder
	defs      DefinitionSource
	repos     RepoSource
	harnesses HarnessSource
	forge     Forge
	// publisher performs the git half of a publication. There is no
	// not-implemented stub for it (baked decision 8): this plan wires a real
	// one everywhere, and a stub would be dead code with no call site.
	publisher  GitPublisher
	principals PrincipalSource
	params     Params
}

func NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder,
	defs DefinitionSource, repos RepoSource, harnesses HarnessSource, forge Forge,
	publisher GitPublisher, principals PrincipalSource, p Params,
) *Service {
	return &Service{
		pool: pool, clock: c, audit: rec, defs: defs, repos: repos,
		harnesses: harnesses, forge: forge, publisher: publisher,
		principals: principals, params: p,
	}
}

// AppendRuntimeEvent records one bounded runtime observation (log
// truncation, egress denial) in its own transaction. The runner calls
// this mid-attempt; volume is capped by the caller (plan: ≤20 denials
// per attempt), so bulk agent output can never bloat the dispatch
// database (spec §3 collection).
func (s *Service) AppendRuntimeEvent(ctx context.Context, ref AttemptRef, kind string, payload map[string]any) error {
	projectID, err := store.New(s.pool).GetRunProjectID(ctx, ref.RunID)
	if err != nil {
		return fmt.Errorf("runs: runtime event project: %w", err)
	}
	now := s.clock.Now()
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		return appendEvents(ctx, tx, now, []Event{{
			RunID: ref.RunID, ProjectID: projectID, Kind: kind,
			CorrelationID: ref.String(), Payload: payload,
		}})
	})
}

func actorID(p authn.Principal) uuid.NullUUID {
	return uuid.NullUUID{UUID: p.UserID, Valid: true}
}

func notFound(err error, msg string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return xerrors.E(xerrors.ClassNotFound, msg)
	}
	return err
}

// unmarshalMap decodes a jsonb column into a map, tolerating NULL/empty.
func unmarshalMap(raw []byte) (map[string]any, error) {
	out := map[string]any{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("runs: unmarshal jsonb: %w", err)
	}
	return out, nil
}

// snapshotFromRow re-parses the pinned snapshot; corruption is internal.
// A stored schema-1 document (Plan 05..07) is upgraded in place: it predates
// AllowSelfApproval, and the shipped baseline value is what those runs were
// created under, so defaulting to true changes nothing for them.
func snapshotFromRow(raw []byte) (PolicySnapshot, error) {
	var s PolicySnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return PolicySnapshot{}, fmt.Errorf("runs: pinned snapshot corrupt: %w", err)
	}
	switch s.SchemaVersion {
	case 1:
		s.SchemaVersion = snapshotSchemaVersion
		s.AllowSelfApproval = true
	case snapshotSchemaVersion:
	default:
		return PolicySnapshot{}, fmt.Errorf("runs: pinned snapshot schema %d is not readable by this binary", s.SchemaVersion)
	}
	return s, nil
}

// outcomeFromRow decodes the optional outcome column.
func outcomeFromRow(raw []byte) (*Outcome, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var o Outcome
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("runs: outcome corrupt: %w", err)
	}
	return &o, nil
}

// compilePinned parses a stored document mirror out of raw store bytes.
// Failures are internal_error by definition (plan Task 3 note).
func compilePinned(def PinnedDefinition) (ExecPlan, error) {
	plan, err := Compile(def.Doc)
	if err != nil {
		return ExecPlan{}, fmt.Errorf("runs: stored definition %s: %w", def.VersionID, err)
	}
	return plan, nil
}
