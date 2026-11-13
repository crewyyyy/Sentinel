package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/privacy"
	"sentinel/internal/storage"
)

const maxHomeChats = 20

type controlPanel struct {
	notify.HomePanel
	Chat, Owner, Receipt int64
	Expires              time.Time
}

func readControlPanel(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64, lock bool) (controlPanel, error) {
	p := controlPanel{}
	query := `SELECT id,chat_id,owner_user_id,COALESCE(telegram_message_id,0),view,page,revision,expires_at FROM control_panels WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	err := db.QueryRow(ctx, query, id).Scan(&p.ID, &p.Chat, &p.Owner, &p.Receipt, &p.View, &p.Page, &p.Revision, &p.Expires)
	return p, err
}

func (e *Engine) controlPanelAllowed(p controlPanel) bool {
	return e.allowed(p.Owner) && e.isAdminDestination(p.Chat) && (p.Chat < 0 || p.Chat == p.Owner) && p.Expires.After(time.Now())
}

// Only currently authorized chats contribute names or counters. A stale menu is
// never a cached grant to reveal another chat's status after rights are revoked.
func (e *Engine) homeData(ctx context.Context, owner int64, view string) (notify.HomeData, error) {
	d := notify.HomeData{AIEnabled: e.Config.AI.Enabled, MediaEnabled: e.Config.Media.Enabled, AutoDiscover: e.Config.Telegram.AutoDiscoverChats, BotUsername: e.Username}
	if view == "help" {
		return d, nil
	}
	if err := e.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM chat_connection_requests WHERE owner_user_id=$1 AND status='pending' AND expires_at>now() AND $2::boolean`, owner, d.AutoDiscover).Scan(&d.PendingRequests); err != nil {
		return d, err
	}
	if view == "connect" || view == "requests" {
		return d, nil
	}
	ids, err := e.chatIDs(ctx, e.Store.Pool)
	if err != nil {
		return d, err
	}
	if len(ids) > maxHomeChats {
		d.Incomplete = true
		ids = ids[:maxHomeChats]
	}
	visible := []int64{}
	for _, chat := range ids {
		allowed, authErr := e.authorized(ctx, owner, chat)
		if authErr != nil {
			d.Incomplete = true
			continue
		}
		if !allowed {
			continue
		}
		var title string
		if err = e.Store.Pool.QueryRow(ctx, `SELECT COALESCE(NULLIF((SELECT title FROM chats WHERE chat_id=$1),''),(SELECT title FROM chat_connection_requests WHERE chat_id=$1 ORDER BY updated_at DESC LIMIT 1),'')`, chat).Scan(&title); err != nil {
			return d, err
		}
		if strings.TrimSpace(title) == "" {
			title = fmt.Sprintf("Группа %d", len(visible)+1)
		}
		d.ChatTitles = append(d.ChatTitles, privacy.Limit(privacy.Redact(title), 100))
		visible = append(visible, chat)
	}
	if view != "status" {
		return d, nil
	}
	if err = e.Store.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status IN('pending','processing')),count(*) FILTER(WHERE status='done') FROM ai_jobs WHERE chat_id=ANY($1::bigint[])`, visible).Scan(&d.PendingReviews, &d.CompletedReviews); err != nil {
		return d, err
	}
	if e.Config.Media.Enabled {
		if err = e.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM visual_requests WHERE status='pending' AND (chat_id=ANY($1::bigint[]) OR chat_id=$2)`, visible, owner).Scan(&d.PendingMedia); err != nil {
			return d, err
		}
	}
	model, err := storage.CurrentModel(ctx, e.Store.Pool, e.Config.AI.Model)
	if err != nil {
		return d, err
	}
	d.Model = model.Model
	return d, nil
}

func (e *Engine) queueHomePanel(ctx context.Context, tx pgx.Tx, p controlPanel) error {
	d := notify.Delivery{HomePanelID: p.ID, ChatID: p.Chat, Private: true, OperatorOnly: true}
	_, err := tx.Exec(ctx, `INSERT INTO outbox(idempotency_key,kind,payload) VALUES($1,'control_panel',$2) ON CONFLICT(idempotency_key) DO UPDATE SET status=CASE WHEN outbox.status='processing' THEN 'processing' ELSE 'pending' END,updated_at=now(),available_at=now()`, fmt.Sprintf("home-panel:%d", p.ID), d)
	return err
}

func (e *Engine) queuePendingConnections(ctx context.Context, tx pgx.Tx, owner int64) error {
	if !e.Config.Telegram.AutoDiscoverChats {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id,chat_id FROM chat_connection_requests WHERE owner_user_id=$1 AND status='pending' AND expires_at>now() ORDER BY updated_at DESC LIMIT 20`, owner)
	if err != nil {
		return err
	}
	type request struct{ id, chat int64 }
	pending := []request{}
	for rows.Next() {
		var r request
		if err = rows.Scan(&r.id, &r.chat); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range pending {
		if err = e.queueConnection(ctx, tx, r.id, owner, r.chat); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) openHome(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	m := u.Message
	p := controlPanel{Chat: m.ChatID, Owner: *m.UserID, Expires: time.Now().Add(24 * time.Hour)}
	allowed := e.controlPanelAllowed(p)
	if err := e.audit(ctx, tx, u, p.Owner, allowed); err != nil {
		return err
	}
	if !allowed {
		return e.ack(ctx, tx, *m, "Доступ запрещён.")
	}
	if err := e.queuePendingConnections(ctx, tx, p.Owner); err != nil {
		return err
	}
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM control_panels WHERE owner_user_id=$1 AND created_at>now()-interval '1 minute'`, p.Owner).Scan(&recent); err != nil {
		return err
	}
	if recent >= 5 {
		return e.ack(ctx, tx, *m, "Используйте открытую панель или повторите /start через минуту.")
	}
	p.View = "home"
	if e.controlName(m.Content()) == "/chats" {
		p.View = "chats"
	}
	if err := tx.QueryRow(ctx, `INSERT INTO control_panels(chat_id,owner_user_id,view) VALUES($1,$2,$3) RETURNING id,revision`, p.Chat, p.Owner, p.View).Scan(&p.ID, &p.Revision); err != nil {
		return err
	}
	return e.queueHomePanel(ctx, tx, p)
}

func (e *Engine) homeCallback(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	c := u.Callback
	answer := func(text string) error {
		return e.text(ctx, tx, "callback:"+c.ID, notify.Delivery{Text: text}, "callback", c.ID)
	}
	parts := strings.Split(c.Data, ":")
	if len(parts) != 5 {
		return answer("Откройте /start заново.")
	}
	id, err1 := strconv.ParseInt(parts[1], 10, 64)
	revision, err2 := strconv.ParseInt(parts[2], 10, 64)
	value, err3 := strconv.Atoi(parts[4])
	if err1 != nil || err2 != nil || err3 != nil || id <= 0 {
		return answer("Неверная кнопка.")
	}
	p, err := readControlPanel(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return answer("Меню истекло. Откройте /start.")
	}
	if err != nil {
		return err
	}
	allowed := e.controlPanelAllowed(p) && p.Owner == c.UserID && p.Chat == c.ChatID && p.Receipt > 0 && p.Receipt == c.MessageID
	if err = e.audit(ctx, tx, u, c.UserID, allowed); err != nil {
		return err
	}
	if !allowed {
		return answer("Нет доступа или меню истекло. Откройте /start.")
	}
	if p.Revision != revision {
		return answer("Кнопка устарела. Используйте обновлённое меню.")
	}
	action := parts[3]
	if action != "page" && value != 0 {
		return answer("Неверная кнопка.")
	}
	switch action {
	case "home", "chats", "connect", "status", "help":
		p.View = action
		p.Page = 0
	case "page":
		if p.View != "chats" || value < 0 || value >= maxHomeChats/notify.HomeChatsPerPage {
			return answer("Страница недоступна.")
		}
		p.Page = value
	case "requests":
		if err = e.queuePendingConnections(ctx, tx, p.Owner); err != nil {
			return err
		}
		p.View = "requests"
		p.Page = 0
	case "modules", "model":
		// Use the same authorization path as a typed command, without ingesting
		// a fabricated chat message or granting rights from a menu button.
		m := domain.Message{ChatID: p.Chat, ID: u.ID, UserID: &p.Owner, Date: time.Now().UTC(), Text: "/" + action}
		command := domain.Update{ID: u.ID, Message: &m}
		if action == "modules" {
			err = e.modulesCommand(ctx, tx, command, nil)
		} else {
			err = e.modelCommand(ctx, tx, command, nil)
		}
		if err != nil {
			return err
		}
	default:
		return answer("Неизвестная кнопка.")
	}
	p.Revision++
	if _, err = tx.Exec(ctx, `UPDATE control_panels SET view=$2,page=$3,revision=$4,updated_at=now() WHERE id=$1`, p.ID, p.View, p.Page, p.Revision); err != nil {
		return err
	}
	if err = e.queueHomePanel(ctx, tx, p); err != nil {
		return err
	}
	return answer("Готово")
}
