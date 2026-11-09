package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
)

// Ingest commits dedup, a normalized redacted event and the acknowledgement cursor atomically.
func (s *Store) Ingest(ctx context.Context, bot string, u domain.Update) error {
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if len(data) > 256<<10 {
		return fmt.Errorf("normalized update exceeds 256 KiB")
	}
	return execTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO processed_updates(bot_instance_id,update_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, bot, u.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if _, err = tx.Exec(ctx, `INSERT INTO inbox(bot_instance_id,id,payload) VALUES($1,$2,$3)`, bot, u.ID, data); err != nil {
			return err
		}
		if err = Metric(ctx, tx, "sentinel_updates_ingested_total", 1); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_cursor(bot_instance_id,last_update_id) VALUES($1,$2) ON CONFLICT(bot_instance_id) DO UPDATE SET last_update_id=EXCLUDED.last_update_id,updated_at=now()`, bot, u.ID)
		return err
	})
}

// Offset is the last acknowledged ID, not the maximum ever seen. Telegram may randomize IDs after a week idle.
func (s *Store) Offset(ctx context.Context, bot string) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT CASE WHEN updated_at<now()-interval '7 days' THEN -1 ELSE last_update_id END FROM chat_cursor WHERE bot_instance_id=$1),-1)`, bot).Scan(&n)
	return n, err
}
