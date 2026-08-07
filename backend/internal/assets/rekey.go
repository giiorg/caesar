package assets

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

// RekeyCredentials re-wraps every credential row not already under the
// active master key (`caesar admin rekey`). One transaction, rows locked
// FOR UPDATE: concurrent rotations serialize, and a crash mid-run leaves
// every row either old-key-readable or fully rewrapped. The plaintext is
// untouched, so secret_version and fingerprint stay as they were.
// Returns the number of rows re-wrapped. Actor is NULL (system); each
// row audits credential.rekeyed.
func (s *Service) RekeyCredentials(ctx context.Context) (int, error) {
	count := 0
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		rows, err := q.ListCredentialRowsForUpdate(ctx)
		if err != nil {
			return fmt.Errorf("assets: list credentials for rekey: %w", err)
		}
		for _, row := range rows {
			if row.KeyID == s.keys.ActiveKeyID() {
				continue
			}
			aad := []byte(row.ID.String())
			plain, err := s.keys.Open(secrets.Envelope{KeyID: row.KeyID, WrappedDEK: row.WrappedDek, Ciphertext: row.Ciphertext}, aad)
			if err != nil {
				return fmt.Errorf("assets: rekey %s (%s): %w", row.Name, row.ID, err)
			}
			env, err := s.keys.Seal(plain, aad)
			if err != nil {
				return fmt.Errorf("assets: rekey seal %s: %w", row.ID, err)
			}
			if err := q.RewrapCredential(ctx, store.RewrapCredentialParams{
				ID: row.ID, KeyID: env.KeyID, WrappedDek: env.WrappedDEK, Ciphertext: env.Ciphertext,
			}); err != nil {
				return fmt.Errorf("assets: rekey update %s: %w", row.ID, err)
			}
			if err := s.audit.Record(ctx, tx, audit.Entry{
				Action: "credential.rekeyed", SubjectKind: "credential", SubjectID: row.ID.String(),
				CorrelationID: httpx.CorrelationID(ctx),
				Detail:        map[string]any{"from": row.KeyID, "to": env.KeyID},
			}); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
