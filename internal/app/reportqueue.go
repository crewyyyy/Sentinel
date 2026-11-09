package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"html"
	"sentinel/internal/domain"
	"sentinel/internal/privacy"
	"strings"
	"time"
)

func (e *Engine) reportQueue(ctx context.Context, tx pgx.Tx, m domain.Message, chat int64) error {
	rows, err := tx.Query(ctx, `SELECT r.id,r.reporter_id,r.target_message_id,r.explanation,COALESCE((SELECT status FROM ai_jobs j WHERE j.chat_id=r.chat_id AND j.message_id=r.target_message_id ORDER BY id DESC LIMIT 1),'not_requested') FROM user_reports r WHERE r.chat_id=$1 AND r.status='pending' ORDER BY created_at,id LIMIT 10`, chat)
	if err != nil {
		return err
	}
	lines := []string{fmt.Sprintf("Ожидающие жалобы чата %d (не доказательство нарушения):", chat)}
	for rows.Next() {
		var id, reporter, target int64
		var note, state string
		if err = rows.Scan(&id, &reporter, &target, &note, &state); err != nil {
			rows.Close()
			return err
		}
		lines = append(lines, fmt.Sprintf("#%d · reporter %d · message %d · AI %s\n%s\n/reportcase %d", id, reporter, target, state, html.EscapeString(privacy.Limit(privacy.Redact(note), 90)), id))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(lines) == 1 {
		lines = append(lines, "Нет ожидающих жалоб.")
	}
	return e.ack(ctx, tx, m, strings.Join(lines, "\n\n"), chat)
}
func (e *Engine) reportCase(ctx context.Context, tx pgx.Tx, u domain.Update, id int64) error {
	var chat, targetID int64
	var userID *int64
	if err := tx.QueryRow(ctx, `SELECT chat_id,target_message_id,reported_user_id FROM user_reports WHERE id=$1`, id).Scan(&chat, &targetID, &userID); err != nil {
		return err
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT payload FROM message_journal WHERE chat_id=$1 AND message_id=$2`, chat, targetID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT payload->'message' FROM ai_jobs WHERE chat_id=$1 AND message_id=$2 ORDER BY id DESC LIMIT 1`, chat, targetID).Scan(&raw)
	}
	m := domain.Message{ChatID: chat, ID: targetID, UserID: userID, Date: time.Now().UTC(), ContentType: "unknown", Partial: true}
	if err == nil {
		if err = json.Unmarshal(raw, &m); err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	m, a, _, err := e.message(ctx, tx, m, true)
	if err != nil {
		return err
	}
	if e.Config.AI.ReviewReportedMessages {
		a.ReviewRequired = true
		if err = e.enqueue(ctx, tx, m, a, true); err != nil {
			return err
		}
	}
	caseID, err := e.incident(ctx, tx, m, a, nil, true)
	if err != nil {
		return err
	}
	return e.ack(ctx, tx, *u.Message, fmt.Sprintf("Жалоба связана с кейсом #%d. Это повод проверить, не метка нарушения.", caseID), chat)
}
func reviewedReports(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `UPDATE user_reports SET status='reviewed' WHERE (chat_id,target_message_id) IN(SELECT chat_id,message_id FROM incident_messages WHERE incident_id=$1)`, id)
	return err
}
