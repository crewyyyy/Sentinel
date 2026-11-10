package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/internal/moderation"
	"sentinel/internal/notify"
	"sentinel/internal/privacy"
	"sentinel/internal/storage"
)

const reportImmediateLimit = 5

func (e *Engine) allowed(id int64) bool {
	for _, v := range e.Config.Telegram.AllowedAdminIDs {
		if id == v {
			return true
		}
	}
	return false
}
func (e *Engine) isAdminDestination(chat int64) bool {
	if chat == e.Config.Telegram.AdminChatID {
		return true
	}
	for _, id := range e.Config.Telegram.AllowedAdminIDs {
		if chat == id {
			return true
		}
	}
	return false
}
func (e *Engine) authorized(ctx context.Context, id, chat int64) (bool, error) {
	if id <= 0 || !e.allowed(id) {
		return false, nil
	}
	if chat == 0 {
		ids, err := e.chatIDs(ctx, e.Store.Pool)
		return len(ids) == 0, err
	}
	return e.Admins.IsAdmin(ctx, chat, id)
}
func (e *Engine) audit(ctx context.Context, tx pgx.Tx, u domain.Update, id int64, allowed bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO authorization_audit(user_id,update_id,allowed) VALUES($1,$2,$3)`, id, u.ID, allowed)
	return err
}
func (e *Engine) callback(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	c := u.Callback
	if strings.HasPrefix(c.Data, "conn:") {
		return e.connectionCallback(ctx, tx, u)
	}
	if strings.HasPrefix(c.Data, "mdl:") {
		return e.modelCallback(ctx, tx, u)
	}
	parts := strings.Split(c.Data, ":")
	if len(parts) != 3 || parts[0] != "act" {
		return nil
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil
	}
	var chatID int64
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT chat_id,payload FROM incidents WHERE id=$1 FOR UPDATE`, id).Scan(&chatID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ok, err := e.authorized(ctx, c.UserID, chatID)
	if err != nil {
		return err
	}
	var cardMatches bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM alerts WHERE incident_id=$1 AND destination_chat_id=$2 AND telegram_message_id=$3)`, id, c.ChatID, c.MessageID).Scan(&cardMatches); err != nil {
		return err
	}
	ok = ok && cardMatches
	if err = e.audit(ctx, tx, u, c.UserID, ok); err != nil {
		return err
	}
	answer := "Доступ запрещён"
	if ok {
		if err = e.label(ctx, tx, id, c.UserID, parts[1], "callback:"+c.ID, ""); err != nil {
			return err
		}
		answer = "Готово. Закрытые кейсы остаются без изменений."
	}
	return e.text(ctx, tx, "callback:"+c.ID, notify.Delivery{Text: answer}, "callback", c.ID)
}
func (e *Engine) label(ctx context.Context, tx pgx.Tx, id, admin int64, label, key, note string) error {
	switch label {
	case "benign", "spam", "needs_review", "false_positive", "close":
	default:
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1 FOR UPDATE`, id).Scan(&raw); err != nil {
		return err
	}
	var c notify.Card
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if c.Status == "closed" && label != "close" {
		return nil
	}
	status := "open"
	if c.Conflict {
		status = "needs_review"
	}
	if label == "close" {
		status = "closed"
		c.Status = status
	} else {
		if c.Label != "" && c.Label != label {
			c.Conflict = true
			status = "needs_review"
			c.Status = "needs_review (conflicting labels)"
		}
		c.Label = label
	}
	payload := struct {
		Note     string      `json:"note"`
		Snapshot notify.Card `json:"snapshot"`
	}{privacy.Limit(privacy.Redact(note), 500), c}
	tag, err := tx.Exec(ctx, `INSERT INTO feedback_events(idempotency_key,incident_id,admin_user_id,label,ruleset_version,payload) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, key, id, admin, label, c.Assessment.Version, payload)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if label == "false_positive" {
		if err = storage.Metric(ctx, tx, "sentinel_false_positive_feedback_total", 1); err != nil {
			return err
		}
	}
	if label != "needs_review" && !c.Conflict {
		if err = reviewedReports(ctx, tx, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE incidents SET status=$3,payload=$2,updated_at=now() WHERE id=$1`, id, c, status); err != nil {
		return err
	}
	return e.queueCard(ctx, tx, id)
}
func (e *Engine) reportCommand(text string) (string, string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", ""
	}
	cmd := parts[0]
	if i := strings.Index(cmd, "@"); i >= 0 {
		if !strings.EqualFold(cmd[i+1:], e.Username) {
			return "", ""
		}
		cmd = cmd[:i]
	}
	for _, a := range e.Config.Moderation.ReportAliases {
		if strings.EqualFold(cmd, a) {
			return a, privacy.Limit(strings.TrimSpace(strings.TrimPrefix(text, parts[0])), 1000)
		}
	}
	return "", ""
}
func (e *Engine) text(ctx context.Context, tx pgx.Tx, key string, d notify.Delivery, kind, callback string) error {
	p := struct {
		notify.Delivery
		CallbackID string `json:"callback_id,omitempty"`
	}{d, callback}
	_, err := tx.Exec(ctx, `INSERT INTO outbox(idempotency_key,kind,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, key, kind, p)
	return err
}
func (e *Engine) ack(ctx context.Context, tx pgx.Tx, m domain.Message, text string, origin ...int64) error {
	return e.ackHTML(ctx, tx, m, html.EscapeString(privacy.Redact(text)), origin...)
}

func (e *Engine) ackHTML(ctx context.Context, tx pgx.Tx, m domain.Message, text string, origin ...int64) error {
	var source int64
	if len(origin) > 0 {
		source = origin[0]
	} else {
		var err error
		source, err = e.firstChat(ctx, tx)
		if err != nil {
			return err
		}
	}
	return e.text(ctx, tx, fmt.Sprintf("ack:%d:%d", m.ChatID, m.ID), notify.Delivery{ChatID: m.ChatID, Text: text, Private: e.isAdminDestination(m.ChatID), OriginChatID: source}, "text", "")
}
func (e *Engine) report(ctx context.Context, tx pgx.Tx, m domain.Message, reason string) error {
	if m.UserID == nil {
		return nil
	}
	if m.ReplyID == 0 {
		return e.ack(ctx, tx, m, "Отправьте /report ответом на конкретное сообщение.")
	}
	var target domain.Message
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT payload FROM message_journal WHERE chat_id=$1 AND message_id=$2`, m.ChatID, m.ReplyID).Scan(&raw)
	if err == nil {
		if err = json.Unmarshal(raw, &target); err != nil {
			return err
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		if m.Reply != nil {
			target = *m.Reply
			target.Partial = true
		} else {
			target = domain.Message{ChatID: m.ChatID, ID: m.ReplyID, Date: m.Date, ChatType: m.ChatType, Partial: true, ContentType: "unknown"}
		}
	} else {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO user_reports(chat_id,reporter_id,target_message_id,reported_user_id,explanation,payload) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, m.ChatID, *m.UserID, m.ReplyID, target.UserID, reason, struct {
		Partial bool `json:"partial_evidence"`
	}{target.Partial})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return e.ack(ctx, tx, m, "Жалоба уже принята, спасибо.")
	}
	if err = storage.Metric(ctx, tx, "sentinel_reports_total", 1); err != nil {
		return err
	}
	target, a, _, err := e.message(ctx, tx, target, true)
	if err != nil {
		return err
	}
	if e.Config.AI.ReviewReportedMessages {
		a.ReviewRequired = true
		if err = e.enqueue(ctx, tx, target, a, true); err != nil {
			return err
		}
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM user_reports WHERE chat_id=$1 AND reporter_id=$2 AND created_at>now()-interval '1 hour'`, m.ChatID, *m.UserID).Scan(&count); err != nil {
		return err
	}
	if count > reportImmediateLimit {
		if a.Score >= e.Config.Moderation.ScoreThresholds[2] {
			if _, err = e.incident(ctx, tx, target, a, nil, true); err != nil {
				return err
			}
		}
		key := fmt.Sprintf("report-limit:%d:%d:%s", m.ChatID, *m.UserID, m.Date.UTC().Format("2006-01-02T15"))
		if err = e.text(ctx, tx, key, notify.Delivery{ChatID: e.Config.Telegram.AdminChatID, Private: true, OriginChatID: m.ChatID, Text: fmt.Sprintf("Много жалоб от ID %d в чате %d. Дополнительные жалобы сохранены pending; /stats показывает очередь. Массовые жалобы не являются метками нарушения.", *m.UserID, m.ChatID)}, "text", ""); err != nil {
			return err
		}
		return e.ack(ctx, tx, m, "Жалоба сохранена. Частота повышена; администраторы уведомлены сводкой.")
	}
	if _, err = e.incident(ctx, tx, target, a, nil, true); err != nil {
		return err
	}
	return e.ack(ctx, tx, m, "Жалоба принята. Администраторы проверят сообщение.")
}
func (e *Engine) membership(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	m := *u.Membership
	if m.Self {
		_, err := tx.Exec(ctx, `INSERT INTO chats(chat_id,features) VALUES($1,$2) ON CONFLICT(chat_id) DO UPDATE SET features=EXCLUDED.features,updated_at=now()`, m.ChatID, m)
		return err
	}
	ev := moderation.FromMembership(m, u.ID)
	if ev == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO moderation_events(source,source_event_id,chat_id,target_user_id,actor_user_id,action,occurred_at,evidence_level,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, ev.Source, ev.SourceEventID, ev.ChatID, ev.TargetUserID, ev.ActorUserID, ev.Action, ev.OccurredAt, ev.EvidenceLevel, ev); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id,payload FROM incidents WHERE chat_id=$1 AND user_id=$2 AND status<>'closed' AND created_at>=$3::timestamptz-interval '60 minutes' AND created_at<=$3::timestamptz+interval '1 minute' ORDER BY id LIMIT 20 FOR UPDATE`, m.ChatID, m.New.UserID, m.Date)
	if err != nil {
		return err
	}
	type record struct {
		id int64
		c  notify.Card
	}
	cards := []record{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var c notify.Card
		if err = json.Unmarshal(raw, &c); err != nil {
			rows.Close()
			return err
		}
		cards = append(cards, record{id, c})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range cards {
		if len(r.c.Events) >= 3 {
			r.c.Events = r.c.Events[len(r.c.Events)-2:]
		}
		r.c.Events = append(r.c.Events, *ev)
		if _, err = tx.Exec(ctx, `UPDATE incidents SET payload=$2,updated_at=now() WHERE id=$1`, r.id, r.c); err != nil {
			return err
		}
		if err = e.queueCard(ctx, tx, r.id); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) controlName(text string) string {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return ""
	}
	cmd := parts[0]
	if i := strings.Index(cmd, "@"); i >= 0 {
		if !strings.EqualFold(cmd[i+1:], e.Username) {
			return ""
		}
		cmd = cmd[:i]
	}
	switch cmd {
	case "/status", "/stats", "/case", "/feedback", "/tune", "/review", "/forget", "/attest", "/link", "/reports", "/reportcase", "/resolve", "/model", "/models", "/start", "/chats":
		return cmd
	}
	return ""
}
func (e *Engine) command(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	m := u.Message
	if m.UserID == nil {
		return nil
	}
	parts := strings.Fields(m.Content())
	if len(parts) == 0 {
		return nil
	}
	cmd := e.controlName(m.Content())
	if cmd == "" {
		return nil
	}
	if cmd == "/start" || cmd == "/chats" {
		return e.chatsCommand(ctx, tx, u)
	}
	if cmd == "/model" || cmd == "/models" {
		return e.modelCommand(ctx, tx, u, parts[1:])
	}
	chat, err := e.firstChat(ctx, tx)
	if err != nil {
		return err
	}
	var id int64
	if (cmd == "/case" || cmd == "/feedback" || cmd == "/review" || cmd == "/attest" || cmd == "/link" || cmd == "/resolve") && len(parts) > 1 {
		var err error
		id, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return e.ack(ctx, tx, *m, "Нужен числовой ID кейса.")
		}
		err = tx.QueryRow(ctx, `SELECT chat_id FROM incidents WHERE id=$1`, id).Scan(&chat)
		if errors.Is(err, pgx.ErrNoRows) {
			return e.ack(ctx, tx, *m, "Кейс не найден.")
		}
		if err != nil {
			return err
		}
	}
	if cmd == "/reports" && len(parts) > 1 {
		value, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return e.ack(ctx, tx, *m, "Нужен ID управляемого чата.")
		}
		active, err := e.managed(ctx, tx, value)
		if err != nil {
			return err
		}
		if !active {
			return e.ack(ctx, tx, *m, "Нужен ID управляемого чата.")
		}
		chat = value
	}
	if cmd == "/reportcase" && len(parts) > 1 {
		var parseErr error
		id, parseErr = strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil {
			return e.ack(ctx, tx, *m, "Нужен числовой ID жалобы.")
		}
		if err := tx.QueryRow(ctx, `SELECT chat_id FROM user_reports WHERE id=$1`, id).Scan(&chat); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return e.ack(ctx, tx, *m, "Жалоба не найдена.")
			}
			return err
		}
	}
	ok, err := e.authorized(ctx, *m.UserID, chat)
	if err != nil {
		return err
	}
	if err = e.audit(ctx, tx, u, *m.UserID, ok); err != nil {
		return err
	}
	if !ok {
		return e.ack(ctx, tx, *m, "Доступ запрещён.")
	}
	switch cmd {
	case "/reports":
		return e.reportQueue(ctx, tx, *m, chat)
	case "/reportcase":
		if id == 0 {
			return e.ack(ctx, tx, *m, "/reportcase REPORT_ID")
		}
		return e.reportCase(ctx, tx, u, id)
	case "/resolve":
		if id == 0 {
			return e.ack(ctx, tx, *m, "/resolve CASE_ID benign|spam|false_positive [обоснование]")
		}
		return e.resolve(ctx, tx, u, id, parts[2:])
	case "/attest", "/link":
		if id == 0 {
			return e.ack(ctx, tx, *m, "Нужен ID кейса.")
		}
		return e.caseAction(ctx, tx, u, id, cmd, parts[2:])
	case "/status", "/stats":
		var pending, done, reports, inbox int64
		if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM ai_jobs WHERE status IN ('pending','processing')),(SELECT count(*) FROM ai_jobs WHERE status='done'),(SELECT count(*) FROM user_reports WHERE status='pending'),(SELECT count(*) FROM inbox WHERE status<>'done')`).Scan(&pending, &done, &reports, &inbox); err != nil {
			return err
		}
		capabilities := ""
		ids, err := e.chatIDs(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT chat_id,COALESCE(features->>'bot_admin',CASE WHEN features->'new'->>'status' IN('administrator','creator') THEN 'true' WHEN features?'new' THEN 'false' END,'unknown') FROM chats WHERE chat_id=ANY($1::bigint[]) ORDER BY chat_id`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int64
			var flag string
			if err = rows.Scan(&cid, &flag); err != nil {
				rows.Close()
				return err
			}
			state := "статус неизвестен"
			if flag == "true" {
				state = "chat_member включён, ждём реального события"
			} else if flag == "false" {
				state = "нет chat_member: бот не администратор"
			}
			capabilities += fmt.Sprintf("\n<code>%d</code> · %s", cid, state)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		setting, err := storage.CurrentModel(ctx, tx, e.Config.AI.Model)
		if err != nil {
			return err
		}
		aiState := "включён"
		if !e.Config.AI.Enabled {
			aiState = "выключен; обязательные проверки ждут в очереди"
		}
		return e.ackHTML(ctx, tx, *m, notify.StatusHeading()+fmt.Sprintf("\nAI: %s\nМодель: <code>%s</code>\n\nПроверки: %d в очереди · %d завершено\nЖалобы: %d · события в очереди: %d\n\n<b>Получение событий</b>%s\n\n/model · модель AI\n/tune · правила <code>%s</code>", aiState, html.EscapeString(privacy.Redact(setting.Model)), pending, done, reports, inbox, capabilities, html.EscapeString(e.Rules.Version)))
	case "/tune":
		return e.ack(ctx, tx, *m, "Правила — в YAML: analysis.modules, weights и пороги.\n\nСмените moderation.ruleset_version, проверьте validate и replay, затем перезапустите бота. Каталог модулей: sentinel modules.")
	case "/case":
		if id == 0 {
			return e.ack(ctx, tx, *m, "Используйте /case ID.")
		}
		return e.queueCard(ctx, tx, id)
	case "/feedback":
		if id == 0 || len(parts) < 3 {
			return e.ack(ctx, tx, *m, "/feedback ID benign|spam|false_positive|needs_review|close [комментарий]")
		}
		if err = e.label(ctx, tx, id, *m.UserID, parts[2], fmt.Sprintf("command:%s:%d", e.BotID, u.ID), strings.Join(parts[3:], " ")); err != nil {
			return err
		}
		return e.ack(ctx, tx, *m, "Метка сохранена.")
	case "/review":
		if id == 0 {
			return e.ack(ctx, tx, *m, "/review ID")
		}
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1`, id).Scan(&raw); err != nil {
			return err
		}
		var c notify.Card
		if err = json.Unmarshal(raw, &c); err != nil {
			return err
		}
		c.Assessment.ReviewRequired = true
		if err = e.enqueue(ctx, tx, c.Message, c.Assessment, true); err != nil {
			return err
		}
		return e.ack(ctx, tx, *m, "Актуальная ревизия поставлена на AI-проверку (повторный готовый review не дублируется).")
	case "/forget":
		return e.ack(ctx, tx, *m, "Удаление выполняет оператор командой sentinel forget --chat ID --user ID. Активные доказательства и незавершённые обязательные jobs требуют отдельного рассмотрения; см. docs/privacy.md.")
	}
	return nil
}
