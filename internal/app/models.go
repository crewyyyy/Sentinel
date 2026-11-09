package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/privacy"
	"sentinel/internal/review"
	"sentinel/internal/storage"
)

// A runtime model is shared by all chats, so one chat's administrator cannot change policy for the others.
func (e *Engine) modelAdmin(ctx context.Context, id int64) (bool, error) {
	if !e.allowed(id) {
		return false, nil
	}
	for _, chat := range e.Config.Telegram.ChatIDs {
		ok, err := e.authorized(ctx, id, chat)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}
func (e *Engine) modelCommand(ctx context.Context, tx pgx.Tx, u domain.Update, args []string) error {
	m := u.Message
	ok, err := e.modelAdmin(ctx, *m.UserID)
	if err != nil {
		return err
	}
	if err = e.audit(ctx, tx, u, *m.UserID, ok); err != nil {
		return err
	}
	if !ok {
		return e.ack(ctx, tx, *m, "Для смены общей модели нужны allowlist и актуальные права во всех управляемых чатах.")
	}
	setting, err := storage.CurrentModel(ctx, tx, e.Config.AI.Model)
	if err != nil {
		return err
	}
	if e.Catalog == nil {
		return e.ack(ctx, tx, *m, "Каталог моделей недоступен; текущая настройка не изменена.")
	}
	var recent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_panels WHERE owner_user_id=$1 AND created_at>now()-interval '5 seconds')`, *m.UserID).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return e.ack(ctx, tx, *m, "Панель уже открыта. Повторите /model через несколько секунд.")
	}
	catalogCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	ids, catalogErr := e.Catalog.Models(catalogCtx)
	cancel()
	if catalogErr != nil {
		return e.ack(ctx, tx, *m, "Каталог провайдера временно недоступен; модель не изменена. Повторите /model позже.")
	}
	query := privacy.Limit(privacy.Redact(strings.Join(args, " ")), 80)
	state := notify.ModelPanel{Query: query, Selected: -1, Revision: 1, SettingVersion: setting.Version}
	seen := map[string]bool{}
	for _, id := range ids {
		if review.ValidModelID(id) && !seen[id] && strings.Contains(strings.ToLower(id), strings.ToLower(query)) {
			state.Models = append(state.Models, id)
			seen[id] = true
			if len(state.Models) >= review.MaxCatalogModels {
				break
			}
		}
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO model_panels(chat_id,owner_user_id,payload) VALUES($1,$2,$3) RETURNING id`, m.ChatID, *m.UserID, state).Scan(&id); err != nil {
		return err
	}
	return e.queueModelPanel(ctx, tx, id, m.ChatID)
}
func (e *Engine) queueModelPanel(ctx context.Context, tx pgx.Tx, id, chat int64) error {
	d := notify.Delivery{ModelPanelID: id, ChatID: chat, Private: true, OriginChatID: e.Config.Telegram.ChatIDs[0]}
	_, err := tx.Exec(ctx, `INSERT INTO outbox(idempotency_key,kind,payload) VALUES($1,'model_panel',$2) ON CONFLICT(idempotency_key) DO UPDATE SET status=CASE WHEN outbox.status='processing' THEN 'processing' ELSE 'pending' END,updated_at=now(),available_at=now()`, fmt.Sprintf("model-panel:%d", id), d)
	return err
}
func (e *Engine) modelCallback(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	c := u.Callback
	parts := strings.Split(c.Data, ":")
	answer := func(text string) error {
		return e.text(ctx, tx, "callback:"+c.ID, notify.Delivery{Text: text}, "callback", c.ID)
	}
	if len(parts) != 5 {
		return answer("Неверная кнопка. Откройте /model.")
	}
	id, idErr := strconv.ParseInt(parts[1], 10, 64)
	version, versionErr := strconv.ParseInt(parts[2], 10, 64)
	value, valueErr := strconv.Atoi(parts[4])
	if idErr != nil || versionErr != nil || valueErr != nil || id <= 0 {
		return answer("Неверная кнопка. Откройте /model.")
	}
	var chat, owner, receipt int64
	var raw []byte
	var expires time.Time
	err := tx.QueryRow(ctx, `SELECT chat_id,owner_user_id,COALESCE(telegram_message_id,0),payload,expires_at FROM model_panels WHERE id=$1 FOR UPDATE`, id).Scan(&chat, &owner, &receipt, &raw, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return answer("Панель истекла. Откройте /model.")
	}
	if err != nil {
		return err
	}
	ok := owner == c.UserID && chat == c.ChatID && receipt > 0 && receipt == c.MessageID && e.isAdminDestination(chat)
	if ok {
		ok, err = e.modelAdmin(ctx, c.UserID)
		if err != nil {
			return err
		}
	}
	if err = e.audit(ctx, tx, u, c.UserID, ok); err != nil {
		return err
	}
	if !ok {
		return answer("Доступ запрещён или кнопка принадлежит другой панели.")
	}
	var state notify.ModelPanel
	if err = json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if expires.Before(time.Now()) || version != state.Revision {
		return answer("Кнопка устарела. Используйте актуальную панель или /model.")
	}
	pages := max(1, (len(state.Models)+notify.ModelsPerPage-1)/notify.ModelsPerPage)
	switch parts[3] {
	case "p":
		if value < 0 || value >= pages {
			return answer("Страница недоступна.")
		}
		state.Page = value
		state.Selected = -1
		state.Notice = ""
	case "s":
		if value < 0 || value >= len(state.Models) || value/notify.ModelsPerPage != state.Page {
			return answer("Модель недоступна на этой странице.")
		}
		current, err := storage.CurrentModel(ctx, tx, e.Config.AI.Model)
		if err != nil {
			return err
		}
		state.Selected = value
		state.SettingVersion = current.Version
		state.Notice = ""
	case "a":
		if value < 0 || value >= len(state.Models) || state.Selected != value {
			return answer("Сначала выберите модель и подтвердите её.")
		}
		// Serialize independent admin panels, including the initially absent settings row.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(824915)`); err != nil {
			return err
		}
		current, err := storage.CurrentModel(ctx, tx, e.Config.AI.Model)
		if err != nil {
			return err
		}
		if current.Version != state.SettingVersion {
			return answer("Другой администратор уже сменил модель. Откройте /model заново.")
		}
		chosen := state.Models[value]
		if !review.ValidModelID(chosen) {
			return answer("Недопустимая модель.")
		}
		if current.Model != chosen {
			if _, err = tx.Exec(ctx, `INSERT INTO runtime_ai_model(id,model,version) VALUES(true,$1,$2) ON CONFLICT(id) DO UPDATE SET model=EXCLUDED.model,version=EXCLUDED.version,updated_at=now()`, chosen, current.Version+1); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO model_change_events(idempotency_key,admin_user_id,old_model,new_model) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, fmt.Sprintf("model:%s:%d", e.BotID, u.ID), c.UserID, current.Model, chosen); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE ai_provider_state SET failures=0,retry_at=now(); UPDATE ai_jobs SET available_at=now() WHERE status='pending' AND last_error<>''`); err != nil {
				return err
			}
			state.SettingVersion = current.Version + 1
		}
		state.Selected = -1
		state.Notice = "Модель сохранена. Новые попытки используют выбранную модель; текущие запросы не прерваны."
	default:
		return answer("Неизвестная кнопка. Откройте /model.")
	}
	state.Revision++
	if _, err = tx.Exec(ctx, `UPDATE model_panels SET payload=$2,updated_at=now() WHERE id=$1`, id, state); err != nil {
		return err
	}
	if err = e.queueModelPanel(ctx, tx, id, chat); err != nil {
		return err
	}
	return answer("Панель обновлена; смена применяется только после подтверждения.")
}
