package app

import (
	"context"
	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
)

func (e *Engine) configuredChat(chat int64) bool {
	for _, id := range e.Config.Telegram.ChatIDs {
		if id == chat {
			return true
		}
	}
	return false
}

// PostgreSQL is authoritative for runtime consent, including a durable inactive tombstone after removal.
// No mutable in-memory allowlist can race the poller or lose consent state on restart.
func (e *Engine) managed(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, chat int64) (bool, error) {
	if chat >= 0 || e.isAdminDestination(chat) {
		return false, nil
	}
	var active bool
	err := db.QueryRow(ctx, `SELECT COALESCE((SELECT active FROM managed_chats WHERE chat_id=$1),$2::boolean)`, chat, e.configuredChat(chat)).Scan(&active)
	return active, err
}

// Exclude a queued pre-consent backlog; a newly observed edit is judged by edit time.
// Telegram dates have second precision, so no stronger ordering within one second is claimed.
func (e *Engine) observeMessage(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, m domain.Message) (bool, error) {
	if m.ChatID >= 0 || e.isAdminDestination(m.ChatID) {
		return false, nil
	}
	at := m.Date
	if m.EditedAt != nil {
		at = *m.EditedAt
	}
	var allowed bool
	err := db.QueryRow(ctx, `SELECT COALESCE((SELECT active AND (source='configured' OR approved_at IS NULL OR $3::timestamptz>=date_trunc('second',approved_at)) FROM managed_chats WHERE chat_id=$1),$2::boolean)`, m.ChatID, e.configuredChat(m.ChatID), at).Scan(&allowed)
	return allowed, err
}
func (e *Engine) chatIDs(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]int64, error) {
	rows, err := db.Query(ctx, `WITH candidates AS (SELECT chat_id FROM managed_chats WHERE active UNION SELECT id FROM unnest($1::bigint[]) id WHERE NOT EXISTS(SELECT 1 FROM managed_chats m WHERE m.chat_id=id)) SELECT chat_id FROM candidates ORDER BY array_position($1::bigint[],chat_id) NULLS LAST,chat_id`, e.Config.Telegram.ChatIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		if !e.isAdminDestination(id) {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}
func (e *Engine) firstChat(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (int64, error) {
	ids, err := e.chatIDs(ctx, db)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return ids[0], nil
}
