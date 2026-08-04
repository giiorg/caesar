package billing

import pgx "github.com/jackc/pgx/v5"

// Test files may export tx-scoped seams to their external test package;
// no other package can reach them, so the analyzer reports nothing here.

func AppendForTest(tx pgx.Tx) error { return nil }
