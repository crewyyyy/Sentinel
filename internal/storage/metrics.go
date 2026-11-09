package storage

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Metric updates a durable monotonic counter in its event's transaction.
func Metric(ctx context.Context, tx pgx.Tx, name string, n int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO runtime_metrics(name,value) VALUES($1,$2) ON CONFLICT(name) DO UPDATE SET value=runtime_metrics.value+EXCLUDED.value`, name, n)
	return err
}
