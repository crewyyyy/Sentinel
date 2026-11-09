package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"sentinel/internal/notify"
	"sentinel/internal/review"
	"sentinel/internal/storage"
	"sentinel/internal/telegram"
)

func (e *Engine) ReviewOne(ctx context.Context, p review.Provider) (bool, error) {
	if !e.Config.AI.Enabled {
		return false, nil
	}
	j, err := e.Store.ClaimAI(ctx, e.Config.AI.OptionalDailyRequestLimit, 4*e.Config.AISubsystemTimeout()+time.Minute, e.Config.AI.Model)
	if err != nil || j == nil {
		return false, err
	}
	started := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, e.Config.AISubsystemTimeout())
	r, err := p.Review(callCtx, j.Payload)
	cancel()
	if err != nil {
		delay := min(30*time.Minute, time.Second*time.Duration(1<<min(10, j.Attempts)))
		code := "provider_failed"
		var api *review.Error
		if errors.As(err, &api) {
			delay = max(delay, api.RetryAfter)
			code = api.Code
		}
		slog.Warn("AI review pending", "job_id", j.ID, "chat_id", j.Payload.Message.ChatID, "message_id", j.Payload.Message.ID, "provider", "openai_compatible", "rule_version", j.Payload.Assessment.Version, "error_code", code, "elapsed_ms", time.Since(started).Milliseconds())
		return true, e.Store.RetryAI(ctx, j, delay, code)
	}
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback(ctx)
	if err = storage.FinishAI(ctx, tx, j, r); err != nil {
		return true, err
	}
	if err = storage.Metric(ctx, tx, "sentinel_ai_latency_ms_total", time.Since(started).Milliseconds()); err != nil {
		return true, err
	}
	m, a := j.Payload.Message, j.Payload.Assessment
	var currentVersion int
	err = tx.QueryRow(ctx, `SELECT edit_version FROM message_keys WHERE chat_id=$1 AND message_id=$2 FOR SHARE`, m.ChatID, m.ID).Scan(&currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, tx.Commit(ctx)
	}
	if err != nil {
		return true, err
	}
	if currentVersion != m.Version {
		return true, tx.Commit(ctx)
	}
	var existing bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM incident_messages WHERE chat_id=$1 AND message_id=$2)`, m.ChatID, m.ID).Scan(&existing); err != nil {
		return true, err
	}
	abusive := r.Result.Category != "legitimate" && r.Result.Category != "benign_criticism" && r.Result.Category != "uncertain"
	if existing || abusive || r.Result.NeedsHumanReview && a.Score >= e.Config.Moderation.AlertThreshold {
		if _, err = e.incident(ctx, tx, m, a, &r, false); err != nil {
			return true, err
		}
	}
	return true, tx.Commit(ctx)
}

type Notifier interface {
	PrivateDestination(context.Context, int64) (bool, error)
	Send(context.Context, int64, string, *models.InlineKeyboardMarkup) (int64, error)
	Edit(context.Context, int64, int64, string, *models.InlineKeyboardMarkup) error
	AnswerCallback(context.Context, string, string) error
}

func (e *Engine) DeliverOne(ctx context.Context, n Notifier) (bool, error) {
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var raw []byte
	var kind string
	var token int
	var changed time.Time
	err = tx.QueryRow(ctx, `SELECT id,payload,kind,attempts+1,updated_at FROM outbox WHERE (status='pending' AND available_at<=now()) OR (status='processing' AND lease_until<now()) ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &raw, &kind, &token, &changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE outbox SET status='processing',attempts=$2::int,lease_token=$2::bigint,lease_until=now()+interval '90 seconds' WHERE id=$1`, id, token); err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	var d struct {
		notify.Delivery
		CallbackID string `json:"callback_id"`
	}
	if err = json.Unmarshal(raw, &d); err != nil {
		return true, err
	}
	if d.Private && d.IncidentID == 0 && d.ModelPanelID == 0 && kind != "callback" {
		private, checkErr := n.PrivateDestination(ctx, d.ChatID)
		if checkErr != nil {
			return true, checkErr
		}
		if !private {
			_, err = e.Store.Pool.Exec(ctx, `UPDATE outbox SET status='pending',lease_until=NULL,available_at=now()+interval '1 hour',last_error='destination_not_private' WHERE id=$1 AND lease_token=$2`, id, token)
			return true, err
		}
		if d.ChatID > 0 {
			allowed, authErr := e.authorized(ctx, d.ChatID, d.OriginChatID)
			if authErr != nil {
				return true, authErr
			}
			if !allowed {
				_, err = e.Store.Pool.Exec(ctx, `UPDATE outbox SET status='done',lease_until=NULL,last_error='recipient_not_authorized',result='{"delivery":"unavailable"}' WHERE id=$1 AND lease_token=$2`, id, token)
				return true, err
			}
		}
	}
	text := d.Text
	var kb *models.InlineKeyboardMarkup
	var msgID int64
	if d.ModelPanelID > 0 {
		var panel []byte
		var owner, chat int64
		var expires time.Time
		err = e.Store.Pool.QueryRow(ctx, `SELECT payload,owner_user_id,chat_id,COALESCE(telegram_message_id,0),expires_at FROM model_panels WHERE id=$1`, d.ModelPanelID).Scan(&panel, &owner, &chat, &msgID, &expires)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return true, err
		}
		allowed := err == nil && chat == d.ChatID && e.isAdminDestination(chat) && expires.After(time.Now())
		if allowed {
			allowed, err = e.modelAdmin(ctx, owner)
			if err != nil {
				return true, err
			}
		}
		if !allowed {
			_, err = e.Store.Pool.Exec(ctx, `UPDATE outbox SET status='done',lease_until=NULL,last_error='panel_expired_or_recipient_not_authorized',result='{"delivery":"unavailable"}' WHERE id=$1 AND lease_token=$2`, id, token)
			return true, err
		}
		private, checkErr := n.PrivateDestination(ctx, chat)
		if checkErr != nil {
			return true, checkErr
		}
		if !private {
			_, err = e.Store.Pool.Exec(ctx, `UPDATE outbox SET status='pending',lease_until=NULL,available_at=now()+interval '1 hour',last_error='destination_not_private' WHERE id=$1 AND lease_token=$2`, id, token)
			return true, err
		}
		var state notify.ModelPanel
		if err = json.Unmarshal(panel, &state); err != nil {
			return true, err
		}
		setting, err := storage.CurrentModel(ctx, e.Store.Pool, e.Config.AI.Model)
		if err != nil {
			return true, err
		}
		text, kb = notify.RenderModels(d.ModelPanelID, state, setting.Model)
	}
	if d.IncidentID > 0 {
		var card []byte
		if err = e.Store.Pool.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1`, d.IncidentID).Scan(&card); err != nil {
			return true, err
		}
		var c notify.Card
		if err = json.Unmarshal(card, &c); err != nil {
			return true, err
		}
		if d.ChatID > 0 {
			authorized, authErr := e.authorized(ctx, d.ChatID, c.Message.ChatID)
			if authErr != nil {
				return true, authErr
			}
			if !authorized {
				_, err = e.Store.Pool.Exec(ctx, `UPDATE outbox SET status='done',lease_until=NULL,last_error='recipient_not_authorized',result='{"delivery":"unavailable"}' WHERE id=$1 AND lease_token=$2`, id, token)
				return true, err
			}
		}
		private, checkErr := n.PrivateDestination(ctx, d.ChatID)
		if checkErr != nil {
			return true, checkErr
		}
		if !private {
			_, err = e.Store.Pool.Exec(ctx, `UPDATE outbox SET status='pending',lease_until=NULL,available_at=now()+interval '1 hour',last_error='destination_not_private' WHERE id=$1 AND lease_token=$2`, id, token)
			return true, err
		}
		text, kb = notify.Render(c)
		err = e.Store.Pool.QueryRow(ctx, `SELECT telegram_message_id FROM alerts WHERE incident_id=$1 AND destination_chat_id=$2 ORDER BY id DESC LIMIT 1`, d.IncidentID, d.ChatID).Scan(&msgID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return true, err
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	if kind == "callback" {
		err = n.AnswerCallback(requestCtx, d.CallbackID, text)
	} else if msgID > 0 {
		err = n.Edit(requestCtx, d.ChatID, msgID, text, kb)
		if err != nil && errors.Is(err, bot.ErrorBadRequest) {
			if strings.Contains(err.Error(), "message is not modified") {
				err = nil
			} else {
				msgID, err = n.Send(requestCtx, d.ChatID, "Обновление статуса (предыдущая карточка недоступна):\n"+text, kb)
			}
		}
	} else {
		msgID, err = n.Send(requestCtx, d.ChatID, text, kb)
	}
	cancel()
	if kind == "callback" && errors.Is(err, bot.ErrorBadRequest) {
		_, dbErr := e.Store.Pool.Exec(ctx, `UPDATE outbox SET status='done',lease_until=NULL,last_error='callback_expired_not_delivered',result='{"delivery":"unavailable"}' WHERE id=$1 AND lease_token=$2`, id, token)
		return true, dbErr
	}
	if err != nil {
		delay := max(telegram.RetryDelay(err), min(30*time.Minute, time.Duration(1<<min(token, 10))*time.Second))
		_, dbErr := e.Store.Pool.Exec(ctx, `UPDATE outbox SET status='pending',lease_until=NULL,available_at=now()+$3::interval,last_error='delivery_failed_or_outcome_unknown' WHERE id=$1 AND lease_token=$2`, id, token, delay.String())
		if dbErr != nil {
			return true, dbErr
		}
		return true, nil
	}
	finish, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer finish.Rollback(ctx)
	tag, err := finish.Exec(ctx, `UPDATE outbox SET status=CASE WHEN updated_at>$3 THEN 'pending' ELSE 'done' END,lease_until=NULL,result=$4 WHERE id=$1 AND lease_token=$2 AND status='processing'`, id, token, changed, struct {
		MessageID int64 `json:"message_id"`
	}{msgID})
	if err != nil {
		return true, err
	}
	if tag.RowsAffected() != 1 {
		return true, fmt.Errorf("outbox lease lost; delivery outcome may be ambiguous")
	}
	if d.ModelPanelID > 0 {
		if _, err = finish.Exec(ctx, `UPDATE model_panels SET telegram_message_id=$2 WHERE id=$1`, d.ModelPanelID, msgID); err != nil {
			return true, err
		}
	}
	if d.IncidentID > 0 {
		_, err = finish.Exec(ctx, `INSERT INTO alerts(incident_id,destination_chat_id,telegram_message_id,idempotency_key,status) VALUES($1,$2,$3,$4,'sent') ON CONFLICT(idempotency_key) DO UPDATE SET telegram_message_id=EXCLUDED.telegram_message_id,status='sent',updated_at=now()`, d.IncidentID, d.ChatID, msgID, fmt.Sprintf("card:%d:%d", d.IncidentID, d.ChatID))
		if err != nil {
			return true, err
		}
	}
	return true, finish.Commit(ctx)
}

// Pending jobs retain minimal redacted evidence beyond the recent journal TTL until completion.
// Completed case evidence has its separate, finite incident retention.
func (e *Engine) Cleanup(ctx context.Context) error {
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	statements := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM outbox WHERE kind='model_panel' AND (payload->>'model_panel_id')::bigint IN(SELECT id FROM model_panels WHERE expires_at<now()) AND status<>'processing'`, nil},
		{`DELETE FROM model_panels WHERE expires_at<now() AND NOT EXISTS(SELECT 1 FROM outbox WHERE kind='model_panel' AND (payload->>'model_panel_id')::bigint=model_panels.id AND status='processing')`, nil},
		{`DELETE FROM model_change_events WHERE created_at<now()-interval '90 days'`, nil},
		{`DELETE FROM feedback_events WHERE incident_id IN(SELECT id FROM incidents WHERE retain_until<now())`, nil},
		{`DELETE FROM alerts WHERE incident_id IN(SELECT id FROM incidents WHERE retain_until<now())`, nil},
		{`DELETE FROM outbox WHERE (payload->>'incident_id')::bigint IN(SELECT id FROM incidents WHERE retain_until<now())`, nil},
		{`DELETE FROM incidents WHERE retain_until<now()`, nil},
		{`DELETE FROM message_journal WHERE (chat_id,message_id) IN (SELECT chat_id,message_id FROM (SELECT chat_id,message_id,row_number() OVER(PARTITION BY chat_id ORDER BY occurred_at DESC,message_id DESC) n FROM message_journal) ranked WHERE n>$1) OR retention_until<now()`, []any{e.Config.Retention.MaxRecentMessagesPerChat}},
		{`DELETE FROM risk_assessments WHERE created_at<now()-$1::interval AND NOT EXISTS(SELECT 1 FROM incident_messages im WHERE im.chat_id=risk_assessments.chat_id AND im.message_id=risk_assessments.message_id)`, []any{fmt.Sprintf("%d days", e.Config.Retention.MessagesDays)}},
		{`DELETE FROM ai_reviews WHERE job_id IN(SELECT id FROM ai_jobs WHERE status='done' AND updated_at<now()-$1::interval)`, []any{fmt.Sprintf("%d days", e.Config.Retention.IncidentDays)}},
		{`DELETE FROM ai_jobs WHERE status IN ('done','failed') AND updated_at<now()-$1::interval`, []any{fmt.Sprintf("%d days", e.Config.Retention.IncidentDays)}},
		{`DELETE FROM daily_activity WHERE day<(now() AT TIME ZONE 'UTC')::date-$1::int`, []any{e.Config.Retention.AggregateDays}},
		{`DELETE FROM user_reports WHERE created_at<now()-$1::interval`, []any{fmt.Sprintf("%d days", e.Config.Retention.IncidentDays)}},
		{`DELETE FROM moderation_events WHERE created_at<now()-$1::interval`, []any{fmt.Sprintf("%d days", e.Config.Retention.IncidentDays)}},
		{`DELETE FROM outbox WHERE status='done' AND updated_at<now()-interval '7 days' AND kind<>'alert'`, nil},
		{`DELETE FROM authorization_audit WHERE created_at<now()-interval '30 days'`, nil},
		{`DELETE FROM message_keys WHERE created_at<now()-$1::interval AND NOT EXISTS(SELECT 1 FROM ai_jobs j WHERE j.chat_id=message_keys.chat_id AND j.message_id=message_keys.message_id AND j.status IN('pending','processing'))`, []any{fmt.Sprintf("%d days", e.Config.Retention.AggregateDays)}},
		{`DELETE FROM ai_budget WHERE day<(now() AT TIME ZONE 'UTC')::date-90`, nil},
		{`DELETE FROM inbox WHERE status='done' AND updated_at<now()-interval '7 days'`, nil},
		{`DELETE FROM processed_updates WHERE created_at<now()-interval '90 days' AND NOT EXISTS(SELECT 1 FROM inbox i WHERE i.bot_instance_id=processed_updates.bot_instance_id AND i.id=processed_updates.update_id)`, nil},
	}
	for _, q := range statements {
		if _, err = tx.Exec(ctx, q.sql, q.args...); err != nil {
			return fmt.Errorf("retention transaction: %w", err)
		}
	}
	return tx.Commit(ctx)
}
