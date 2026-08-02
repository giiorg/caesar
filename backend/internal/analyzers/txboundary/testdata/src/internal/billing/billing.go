package billing

import pgx "github.com/jackc/pgx/v5"

type Service struct{}

func (s *Service) Charge(tx pgx.Tx, amount int) error { return nil } // want `transaction type in exported bounded-context API`

func ChargeAll(txs []pgx.Tx) error { return nil } // want `transaction type in exported bounded-context API`

type Port interface {
	Settle(tx pgx.Tx) error // want `transaction type in exported bounded-context API`
}

func internalHelper(tx pgx.Tx) error { return nil } // unexported: fine within the context
