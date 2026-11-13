package app

import (
	"context"
	"encoding/json"
	"fmt"

	"sentinel/internal/domain"
	"sentinel/internal/notify"
)

// A budget pause remains pending, with at most one private notice per request.
func (e *Engine) deferVisualNotice(ctx context.Context, key string) error {
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `UPDATE visual_requests SET deferred_notified=true WHERE status='pending' AND NOT deferred_notified AND chat_id>0 AND $1=ANY(check_keys) RETURNING id,payload`, key)
	if err != nil {
		return err
	}
	type notice struct {
		id int64
		m  domain.Message
	}
	notices := []notice{}
	for rows.Next() {
		var n notice
		var raw []byte
		if err = rows.Scan(&n.id, &raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &n.m); err != nil {
			rows.Close()
			return err
		}
		notices = append(notices, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range notices {
		if e.privateMedia(n.m) {
			if err = e.text(ctx, tx, fmt.Sprintf("media-deferred:%d", n.id), notify.Delivery{ChatID: n.m.ChatID, Private: true, OperatorOnly: true, ReplyToMessageID: n.m.ID, MediaRequestID: n.id, Text: "⏳ <b>Проверка в очереди</b>\nДневной лимит исчерпан. Продолжу после 00:00 UTC."}, "media_result", ""); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
