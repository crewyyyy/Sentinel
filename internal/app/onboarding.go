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
)

func (e *Engine) botID() (int64, bool) {
	id, err := strconv.ParseInt(e.BotID, 10, 64)
	return id, err == nil && id > 0
}
func (e *Engine) discoverable(ctx context.Context, m *domain.Membership) (bool, error) {
	if !m.Self || m.ChatID >= 0 || e.isAdminDestination(m.ChatID) {
		return false, nil
	}
	if e.configuredChat(m.ChatID) {
		return true, nil
	}
	if !e.Config.Telegram.AutoDiscoverChats || (m.ChatType != "group" && m.ChatType != "supergroup") {
		return false, nil
	}
	id, valid := e.botID()
	if !valid || m.New.UserID != id || m.Old.UserID != id {
		return false, nil
	}
	if e.allowed(m.ActorID) {
		return true, nil
	}
	var known bool
	err := e.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_chats WHERE chat_id=$1) OR EXISTS(SELECT 1 FROM chat_connection_requests WHERE chat_id=$1 AND expires_at>now())`, m.ChatID).Scan(&known)
	return known, err
}
func (e *Engine) connectionAudit(ctx context.Context, tx pgx.Tx, u domain.Update, chat, actor int64, action string) error {
	var who *int64
	if actor > 0 {
		who = &actor
	}
	_, err := tx.Exec(ctx, `INSERT INTO chat_connection_events(idempotency_key,chat_id,actor_user_id,action) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, fmt.Sprintf("connection:%s:%d:%s", e.BotID, u.ID, action), chat, who, action)
	return err
}
func (e *Engine) selfMembership(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	m := u.Membership
	if m.ChatID >= 0 || e.isAdminDestination(m.ChatID) {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(824916)`); err != nil {
		return err
	}
	active, err := e.managed(ctx, tx, m.ChatID)
	if err != nil {
		return err
	}
	var latest *time.Time
	if err = tx.QueryRow(ctx, `SELECT max(event_at) FROM (SELECT NULLIF(membership_at,'-infinity'::timestamptz) event_at FROM managed_chats WHERE chat_id=$1 UNION ALL SELECT membership_at FROM chat_connection_requests WHERE chat_id=$1) events`, m.ChatID).Scan(&latest); err != nil {
		return err
	}
	if latest != nil && m.Date.Before(*latest) {
		return nil
	}
	source := "onboarding"
	if e.configuredChat(m.ChatID) {
		source = "configured"
	}
	if m.New.Status == "left" || m.New.Status == "kicked" {
		if !active && latest == nil && !e.configuredChat(m.ChatID) {
			return nil
		}
		if _, err = tx.Exec(ctx, `INSERT INTO managed_chats(chat_id,active,source,membership_at) VALUES($1,false,$2,$3) ON CONFLICT(chat_id) DO UPDATE SET active=false,membership_at=EXCLUDED.membership_at,updated_at=now()`, m.ChatID, source, m.Date); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE chat_connection_requests SET status='cancelled',revision=revision+1,membership_at=$2,updated_at=now() WHERE chat_id=$1 AND status IN('pending','approved')`, m.ChatID, m.Date); err != nil {
			return err
		}
		if err = e.connectionAudit(ctx, tx, u, m.ChatID, m.ActorID, "disconnected"); err != nil {
			return err
		}
		if active {
			return e.membership(ctx, tx, u)
		}
		return nil
	}
	present := m.New.Status == "administrator" || m.New.Status == "creator" || m.New.Status == "member" || (m.New.Status == "restricted" && m.New.IsMember)
	if !present {
		return nil
	}
	if active || e.configuredChat(m.ChatID) {
		if _, err = tx.Exec(ctx, `INSERT INTO managed_chats(chat_id,active,source,membership_at) VALUES($1,true,$2,$3) ON CONFLICT(chat_id) DO UPDATE SET active=true,source=EXCLUDED.source,membership_at=EXCLUDED.membership_at,updated_at=now()`, m.ChatID, source, m.Date); err != nil {
			return err
		}
		return e.membership(ctx, tx, u)
	}
	if !e.Config.Telegram.AutoDiscoverChats {
		return nil
	}
	id, valid := e.botID()
	if !valid || m.Old.UserID != id || m.New.UserID != id || (m.ChatType != "group" && m.ChatType != "supergroup") {
		return nil
	}
	// Only a configured administrator can create a candidate. Live rights are checked before disclosure and activation.
	if !e.allowed(m.ActorID) {
		_, err = tx.Exec(ctx, `UPDATE chat_connection_requests SET membership_at=$2,revision=revision+1,updated_at=now() WHERE chat_id=$1 AND status='pending'`, m.ChatID, m.Date)
		return err
	}
	var request int64
	if err = tx.QueryRow(ctx, `INSERT INTO chat_connection_requests(chat_id,owner_user_id,title,membership_at,chat_type) VALUES($1,$2,$3,$4,$5) ON CONFLICT(chat_id,owner_user_id) DO UPDATE SET title=EXCLUDED.title,chat_type=EXCLUDED.chat_type,status='pending',revision=chat_connection_requests.revision+1,membership_at=EXCLUDED.membership_at,expires_at=now()+interval '24 hours',updated_at=now() RETURNING id`, m.ChatID, m.ActorID, m.ChatTitle, m.Date, m.ChatType).Scan(&request); err != nil {
		return err
	}
	if err = e.connectionAudit(ctx, tx, u, m.ChatID, m.ActorID, "discovered"); err != nil {
		return err
	}
	return e.queueConnection(ctx, tx, request, m.ActorID, m.ChatID)
}
func (e *Engine) queueConnection(ctx context.Context, tx pgx.Tx, id, owner, chat int64) error {
	d := notify.Delivery{ConnectionID: id, ChatID: owner, Private: true, OriginChatID: chat}
	_, err := tx.Exec(ctx, `INSERT INTO outbox(idempotency_key,kind,payload) VALUES($1,'chat_connection',$2) ON CONFLICT(idempotency_key) DO UPDATE SET status=CASE WHEN outbox.status='processing' THEN 'processing' ELSE 'pending' END,updated_at=now(),available_at=now()`, fmt.Sprintf("connection:%d", id), d)
	return err
}
func (e *Engine) connectionCallback(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	c := u.Callback
	answer := func(s string) error {
		return e.text(ctx, tx, "callback:"+c.ID, notify.Delivery{Text: s}, "callback", c.ID)
	}
	parts := strings.Split(c.Data, ":")
	if len(parts) != 4 {
		return answer("Неверная кнопка. Откройте /chats.")
	}
	id, idErr := strconv.ParseInt(parts[1], 10, 64)
	revision, revErr := strconv.ParseInt(parts[2], 10, 64)
	if idErr != nil || revErr != nil || id <= 0 || (parts[3] != "yes" && parts[3] != "no") {
		return answer("Неверная кнопка.")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(824916)`); err != nil {
		return err
	}
	var chat, owner, receipt, currentRevision int64
	var title, status, chatType string
	var expires, memberAt time.Time
	err := tx.QueryRow(ctx, `SELECT chat_id,owner_user_id,COALESCE(telegram_message_id,0),revision,title,status,expires_at,membership_at,chat_type FROM chat_connection_requests WHERE id=$1 FOR UPDATE`, id).Scan(&chat, &owner, &receipt, &currentRevision, &title, &status, &expires, &memberAt, &chatType)
	if errors.Is(err, pgx.ErrNoRows) {
		return answer("Запрос истёк. Добавьте бота заново или откройте /chats.")
	}
	if err != nil {
		return err
	}
	allowed := e.Config.Telegram.AutoDiscoverChats && owner == c.UserID && c.ChatID == owner && receipt > 0 && c.MessageID == receipt
	if allowed {
		allowed, err = e.authorized(ctx, c.UserID, chat)
		if err != nil {
			allowed = false
		}
	}
	if err = e.audit(ctx, tx, u, c.UserID, allowed); err != nil {
		return err
	}
	if !allowed {
		return answer("Доступ запрещён или кнопка принадлежит другому диалогу.")
	}
	if status != "pending" || currentRevision != revision || expires.Before(time.Now()) {
		return answer("Запрос уже обработан или устарел. Откройте /chats.")
	}
	if parts[3] == "no" {
		if _, err = tx.Exec(ctx, `UPDATE chat_connection_requests SET status='rejected',revision=revision+1,updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		if err = e.connectionAudit(ctx, tx, u, chat, c.UserID, "rejected"); err != nil {
			return err
		}
		if err = e.queueConnection(ctx, tx, id, owner, chat); err != nil {
			return err
		}
		return answer("Подключение отклонено. Сообщения не собираются.")
	}
	bot, valid := e.botID()
	if !valid {
		return answer("Не удалось проверить ID бота.")
	}
	botAdmin, err := e.Admins.IsAdmin(ctx, chat, bot)
	if err != nil {
		return answer("Права бота сейчас проверить не удалось. Выдайте статус admin и повторите /chats.")
	}
	if !botAdmin {
		return answer("Сначала выдайте боту статус администратора без прав банить/удалять, затем повторите подтверждение.")
	}
	active, err := e.managed(ctx, tx, chat)
	if err != nil {
		return err
	}
	if active {
		return answer("Чат уже подключён.")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO managed_chats(chat_id,active,source,approved_by,approved_at,membership_at) VALUES($1,true,'onboarding',$2,now(),$3) ON CONFLICT(chat_id) DO UPDATE SET active=true,source='onboarding',approved_by=EXCLUDED.approved_by,approved_at=now(),membership_at=EXCLUDED.membership_at,updated_at=now()`, chat, c.UserID, memberAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chats(chat_id,chat_type,features) VALUES($1,$2,'{"bot_admin":true,"chat_member":"subscribed_requires_real_update"}') ON CONFLICT(chat_id) DO UPDATE SET chat_type=EXCLUDED.chat_type,features=EXCLUDED.features,updated_at=now()`, chat, chatType); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE chat_connection_requests SET status=CASE WHEN id=$2 THEN 'approved' ELSE 'cancelled' END,revision=revision+1,updated_at=now() WHERE chat_id=$1 AND status='pending'`, chat, id); err != nil {
		return err
	}
	if err = e.connectionAudit(ctx, tx, u, chat, c.UserID, "approved"); err != nil {
		return err
	}
	if err = e.queueConnection(ctx, tx, id, owner, chat); err != nil {
		return err
	}
	return answer("Чат подключён. Наблюдение начинается с доступных новых сообщений; старая история не загружается.")
}
func (e *Engine) chatsCommand(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	m := u.Message
	if !e.allowed(*m.UserID) {
		return e.ack(ctx, tx, *m, "Доступ запрещён.")
	}
	if err := e.audit(ctx, tx, u, *m.UserID, true); err != nil {
		return err
	}
	ids, err := e.chatIDs(ctx, tx)
	if err != nil {
		return err
	}
	text := notify.HelpHeading() + "\nПроверяю новые сообщения и присылаю карточки для решения. Кнопки сохраняют метки; бот не банит и не удаляет сообщения.\n\n<b>Подключить группу</b>\nДобавьте бота администратором без прав банить/удалять. Уведомите участников об обработке и внешнем AI, затем подтвердите карточку в ЛС.\n\n<b>Подключённые чаты</b>"
	shown := 0
	unavailable := false
	for _, chat := range ids {
		allowed, err := e.authorized(ctx, *m.UserID, chat)
		if err != nil {
			// Do not disclose identifiers when current rights cannot be verified.
			unavailable = true
			continue
		}
		if allowed && shown < 20 {
			text += fmt.Sprintf("\n<code>%d</code>", chat)
			shown++
		}
	}
	if shown == 0 {
		text += "\nПока нет. Подтвердите заявку в ЛС."
	}
	if unavailable {
		text += "\nЧасть чатов не показана: текущие права проверить не удалось."
	}
	rows, err := tx.Query(ctx, `SELECT id,chat_id FROM chat_connection_requests WHERE owner_user_id=$1 AND status='pending' AND expires_at>now() ORDER BY updated_at DESC LIMIT 20`, *m.UserID)
	if err != nil {
		return err
	}
	type pending struct{ id, chat int64 }
	requests := []pending{}
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.chat); err != nil {
			rows.Close()
			return err
		}
		requests = append(requests, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, request := range requests {
		if err = e.queueConnection(ctx, tx, request.id, *m.UserID, request.chat); err != nil {
			return err
		}
	}
	text += fmt.Sprintf("\n\nЗаявок: %d (до 20 за раз). Карточки придут в ЛС после проверки прав.\n\n/status · состояние\n/model · модель AI\n/report · жалоба ответом в группе\n\n<i>Доступны только новые сообщения. Журнал ограничен; удаление сообщения обычно неизвестно.</i>", len(requests))
	return e.text(ctx, tx, fmt.Sprintf("ack:%d:%d", m.ChatID, m.ID), notify.Delivery{ChatID: m.ChatID, Private: true, OperatorOnly: true, Text: text}, "text", "")
}
