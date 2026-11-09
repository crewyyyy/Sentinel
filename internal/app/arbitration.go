package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/privacy"
	"strings"
)

func (e *Engine) resolve(ctx context.Context, tx pgx.Tx, u domain.Update, id int64, args []string) error {
	if len(args) == 0 {
		return e.ack(ctx, tx, *u.Message, "/resolve CASE_ID benign|spam|false_positive [обоснование]")
	}
	label := args[0]
	switch label {
	case "benign", "spam", "false_positive":
	default:
		return e.ack(ctx, tx, *u.Message, "Для арбитража нужен explicit benign/spam/false_positive.")
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1 FOR UPDATE`, id).Scan(&raw); err != nil {
		return err
	}
	var c notify.Card
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	c.Conflict = false
	c.Label = label
	c.Status = "open"
	if _, err := tx.Exec(ctx, `INSERT INTO feedback_events(idempotency_key,incident_id,admin_user_id,label,source,ruleset_version,payload) VALUES($1,$2,$3,$4,'admin_arbitration',$5,$6) ON CONFLICT DO NOTHING`, fmt.Sprintf("arbitrate:%s:%d", e.BotID, u.ID), id, *u.Message.UserID, label, c.Assessment.Version, struct {
		Note     string      `json:"note"`
		Snapshot notify.Card `json:"snapshot"`
	}{privacy.Limit(privacy.Redact(strings.Join(args[1:], " ")), 500), c}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE incidents SET status='open',payload=$2,updated_at=now() WHERE id=$1`, id, c); err != nil {
		return err
	}
	if err := reviewedReports(ctx, tx, id); err != nil {
		return err
	}
	if err := e.queueCard(ctx, tx, id); err != nil {
		return err
	}
	return e.ack(ctx, tx, *u.Message, "Арбитражная метка записана; это не подтверждение санкции.", c.Message.ChatID)
}
