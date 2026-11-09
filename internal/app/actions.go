package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/privacy"
	"strconv"
	"strings"
	"time"
)

// CaseAction records only an attestation or a human-selected relation. It never sends an enforcement API call.
func (e *Engine) caseAction(ctx context.Context, tx pgx.Tx, u domain.Update, id int64, cmd string, args []string) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1 FOR UPDATE`, id).Scan(&raw); err != nil {
		return err
	}
	var c notify.Card
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if c.Message.UserID == nil {
		return e.ack(ctx, tx, *u.Message, "Target user unknown: подтверждение/связь действия недоступны.")
	}
	var ev domain.ModerationEvent
	note := ""
	if cmd == "/attest" {
		if len(args) == 0 {
			return e.ack(ctx, tx, *u.Message, "/attest CASE_ID warn|ban|restrict|delete [пояснение]. Это только утверждение администратора, не действие Telegram.")
		}
		action := args[0]
		switch action {
		case "warn", "ban", "restrict", "delete", "unrestrict", "kick":
		default:
			return e.ack(ctx, tx, *u.Message, "Неизвестный action; ничего не записано.")
		}
		ev = domain.ModerationEvent{Source: "admin_attested", SourceEventID: fmt.Sprintf("command:%s:%d", e.BotID, u.ID), ChatID: c.Message.ChatID, TargetUserID: *c.Message.UserID, ActorUserID: nil, Action: action, OccurredAt: time.Now().UTC(), EvidenceLevel: "attested"}
		_, err := tx.Exec(ctx, `INSERT INTO moderation_events(source,source_event_id,chat_id,target_user_id,actor_user_id,action,occurred_at,evidence_level,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, ev.Source, ev.SourceEventID, ev.ChatID, ev.TargetUserID, ev.ActorUserID, ev.Action, ev.OccurredAt, ev.EvidenceLevel, ev)
		if err != nil {
			return err
		}
		note = strings.Join(args[1:], " ")
	} else {
		if len(args) == 0 {
			return e.ack(ctx, tx, *u.Message, "/link CASE_ID MODERATION_EVENT_DB_ID")
		}
		eventID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return e.ack(ctx, tx, *u.Message, "Нужен числовой ID события.")
		}
		if err = tx.QueryRow(ctx, `SELECT payload FROM moderation_events WHERE id=$1 AND chat_id=$2 AND target_user_id=$3`, eventID, c.Message.ChatID, *c.Message.UserID).Scan(&raw); err != nil {
			return e.ack(ctx, tx, *u.Message, "Событие не найдено для пользователя/чата кейса.")
		}
		if err = json.Unmarshal(raw, &ev); err != nil {
			return err
		}
		note = "manual relation to event " + args[0]
	}
	related := c.Message.ID
	ev.RelatedMessageID = &related
	found := false
	for i := range c.Events {
		if c.Events[i].Source == ev.Source && c.Events[i].SourceEventID == ev.SourceEventID {
			c.Events[i] = ev
			found = true
		}
	}
	if !found {
		if len(c.Events) >= 3 {
			c.Events = c.Events[len(c.Events)-2:]
		}
		c.Events = append(c.Events, ev)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO feedback_events(idempotency_key,incident_id,admin_user_id,label,source,ruleset_version,payload) VALUES($1,$2,$3,$4,'admin_command',$5,$6) ON CONFLICT DO NOTHING`, fmt.Sprintf("case-action:%s:%d", e.BotID, u.ID), id, *u.Message.UserID, strings.TrimPrefix(cmd, "/"), c.Assessment.Version, struct {
		Event domain.ModerationEvent
		Note  string
	}{ev, privacy.Limit(privacy.Redact(note), 500)}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE incidents SET payload=$2,updated_at=now() WHERE id=$1`, id, c); err != nil {
		return err
	}
	if err := e.queueCard(ctx, tx, id); err != nil {
		return err
	}
	return e.ack(ctx, tx, *u.Message, "Записано как утверждение/связь со слов администратора. Бот не применял санкцию.")
}
