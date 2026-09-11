package storage

import (
	"context"
	"database/sql"
	"time"
)

type authorizationRevisionRepo struct {
	db *sql.DB
}

func (r *authorizationRevisionRepo) Current(ctx context.Context) (uint64, error) {
	if r == nil || r.db == nil {
		return 0, sql.ErrConnDone
	}
	var revision uint64
	err := r.db.QueryRowContext(ctx, `SELECT revision FROM authorization_revision WHERE id=1`).Scan(&revision)
	return revision, err
}

func bumpAuthorizationRevision(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return sql.ErrTxDone
	}
	result, err := tx.ExecContext(ctx, `UPDATE authorization_revision SET revision=revision+1, updated_at=? WHERE id=1`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return checkAffected(result, nil)
}
