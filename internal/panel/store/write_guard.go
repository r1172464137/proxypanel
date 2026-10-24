package store

import (
	"context"
	"database/sql"

	"proxypanel/internal/panel/domain"
)

// requireWritableTx is the sole ordinary-write gate. Call it before any object
// locks; recovery entry uses this same control row, held through commit.
func requireWritableTx(ctx context.Context, tx *sql.Tx) error {
	recovery, err := recoveryModeTx(ctx, tx)
	if err != nil {
		return err
	}
	if recovery {
		return domain.E(domain.CodeRecoveryMode, 409, "panel is in recovery coordination mode; ordinary changes are paused")
	}
	return nil
}

// recoveryModeTx serializes necessary telemetry with recovery transitions too,
// without rejecting telemetry. Always acquire it before object/advisory locks.
func recoveryModeTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var recovery bool
	err := tx.QueryRowContext(ctx, `SELECT recovery_mode FROM control_state WHERE id=1 FOR UPDATE`).Scan(&recovery)
	return recovery, dbErr(err)
}

func (s *Store) inWritableTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := requireWritableTx(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}
