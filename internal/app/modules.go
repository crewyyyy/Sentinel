package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/privacy"
	"sentinel/internal/rules"
	"sentinel/internal/storage"
)

type modulePanelRecord struct {
	Destination, Owner, Receipt int64
	Expires                     time.Time
	State                       notify.ModulePanel
}

func readModulePanel(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64, lock bool) (modulePanelRecord, error) {
	var r modulePanelRecord
	var raw []byte
	query := `SELECT destination_chat_id,owner_user_id,COALESCE(telegram_message_id,0),expires_at,payload FROM module_panels WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	err := db.QueryRow(ctx, query, id).Scan(&r.Destination, &r.Owner, &r.Receipt, &r.Expires, &raw)
	if err != nil {
		return r, err
	}
	if len(raw) > 131072 || json.Unmarshal(raw, &r.State) != nil {
		return r, fmt.Errorf("invalid module panel")
	}
	return r, nil
}

func (e *Engine) moduleChatAllowed(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, user, chat int64) (bool, error) {
	if !e.allowed(user) {
		return false, nil
	}
	active, err := e.managed(ctx, tx, chat)
	if err != nil || !active {
		return false, err
	}
	return e.authorized(ctx, user, chat)
}

func (e *Engine) modulePanelAllowed(ctx context.Context, r modulePanelRecord) (bool, error) {
	baseDigest, err := e.policyDigest()
	if err != nil {
		return false, err
	}
	if !e.allowed(r.Owner) || !e.isAdminDestination(r.Destination) || r.Expires.Before(time.Now()) || r.State.BaseDigest != baseDigest {
		return false, nil
	}
	if r.Destination > 0 && r.Destination != r.Owner {
		return false, nil
	}
	if r.State.Origin != 0 {
		return e.moduleChatAllowed(ctx, e.Store.Pool, r.Owner, r.State.Origin)
	}
	if len(r.State.Chats) == 0 || len(r.State.Chats) > 20 {
		return false, nil
	}
	for _, chat := range r.State.Chats {
		ok, err := e.moduleChatAllowed(ctx, e.Store.Pool, r.Owner, chat.ID)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (e *Engine) moduleTitle(ctx context.Context, tx pgx.Tx, chat int64) (string, error) {
	var title string
	err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF((SELECT title FROM chats WHERE chat_id=$1),''),(SELECT title FROM chat_connection_requests WHERE chat_id=$1 ORDER BY updated_at DESC LIMIT 1),'')`, chat).Scan(&title)
	if title == "" {
		title = fmt.Sprintf("Чат %d", chat)
	}
	return privacy.Limit(privacy.Redact(title), 100), err
}

func (e *Engine) selectModuleChat(ctx context.Context, tx pgx.Tx, p *notify.ModulePanel, chat int64) error {
	setting, err := storage.CurrentModules(ctx, tx, chat)
	if err != nil {
		return err
	}
	title, err := e.moduleTitle(ctx, tx, chat)
	if err != nil {
		return err
	}
	p.Origin = chat
	p.ChatTitle = title
	p.Draft = setting.Overrides
	p.SettingVersion = setting.Version
	p.View = "list"
	p.Dirty = false
	p.Selected = -1
	return nil
}

func (e *Engine) modulesCommand(ctx context.Context, tx pgx.Tx, u domain.Update, args []string) error {
	m := u.Message
	ok := e.allowed(*m.UserID) && e.isAdminDestination(m.ChatID) && (m.ChatID < 0 || m.ChatID == *m.UserID)
	if err := e.audit(ctx, tx, u, *m.UserID, ok); err != nil {
		return err
	}
	if !ok {
		return e.ack(ctx, tx, *m, "Доступ запрещён.")
	}
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM module_panels WHERE owner_user_id=$1 AND created_at>now()-interval '1 minute'`, *m.UserID).Scan(&recent); err != nil {
		return err
	}
	if recent >= 5 {
		return e.ack(ctx, tx, *m, "Используйте открытую панель или повторите /modules через минуту.")
	}
	baseDigest, err := e.policyDigest()
	if err != nil {
		return err
	}
	p := notify.ModulePanel{Revision: 1, BaseDigest: baseDigest, View: "chats", Selected: -1}
	if len(args) > 0 {
		chat, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || len(args) != 1 {
			return e.ack(ctx, tx, *m, "/modules — выбрать чат\n/modules CHAT_ID — открыть его модули")
		}
		allowed, err := e.moduleChatAllowed(ctx, tx, *m.UserID, chat)
		if err != nil {
			return err
		}
		if !allowed {
			return e.ack(ctx, tx, *m, "Нет доступа к управляемому чату.")
		}
		if err = e.selectModuleChat(ctx, tx, &p, chat); err != nil {
			return err
		}
	} else {
		chats, err := e.chatIDs(ctx, tx)
		if err != nil {
			return err
		}
		for i, chat := range chats {
			if i >= 20 {
				break
			}
			allowed, err := e.moduleChatAllowed(ctx, tx, *m.UserID, chat)
			if err != nil {
				return err
			}
			if !allowed {
				continue
			}
			title, err := e.moduleTitle(ctx, tx, chat)
			if err != nil {
				return err
			}
			p.Chats = append(p.Chats, notify.ModuleChat{ID: chat, Title: title})
		}
		if len(p.Chats) == 0 {
			return e.ack(ctx, tx, *m, "Доступных чатов нет. Для другого чата: /modules CHAT_ID.")
		}
		if len(p.Chats) == 1 {
			if err = e.selectModuleChat(ctx, tx, &p, p.Chats[0].ID); err != nil {
				return err
			}
		}
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO module_panels(destination_chat_id,owner_user_id,origin_chat_id,payload) VALUES($1,$2,$3,$4) RETURNING id`, m.ChatID, *m.UserID, p.Origin, p).Scan(&id); err != nil {
		return err
	}
	return e.queueModulePanel(ctx, tx, id, m.ChatID, p.Origin)
}

func (e *Engine) queueModulePanel(ctx context.Context, tx pgx.Tx, id, destination, origin int64) error {
	d := notify.Delivery{ModulePanelID: id, ChatID: destination, Private: true, OriginChatID: origin}
	_, err := tx.Exec(ctx, `INSERT INTO outbox(idempotency_key,kind,payload) VALUES($1,'module_panel',$2) ON CONFLICT(idempotency_key) DO UPDATE SET status=CASE WHEN outbox.status='processing' THEN 'processing' ELSE 'pending' END,updated_at=now(),available_at=now()`, fmt.Sprintf("module-panel:%d", id), d)
	return err
}

func (e *Engine) saveModulePanel(ctx context.Context, tx pgx.Tx, id int64, r *modulePanelRecord) error {
	r.State.Revision++
	if _, err := tx.Exec(ctx, `UPDATE module_panels SET payload=$2,origin_chat_id=$3,updated_at=now() WHERE id=$1`, id, r.State, r.State.Origin); err != nil {
		return err
	}
	return e.queueModulePanel(ctx, tx, id, r.Destination, r.State.Origin)
}

func (e *Engine) moduleCallback(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	c := u.Callback
	answer := func(text string) error {
		return e.text(ctx, tx, "callback:"+c.ID, notify.Delivery{Text: text}, "callback", c.ID)
	}
	parts := strings.Split(c.Data, ":")
	if len(parts) != 5 {
		return answer("Неверная кнопка. Откройте /modules.")
	}
	id, err1 := strconv.ParseInt(parts[1], 10, 64)
	revision, err2 := strconv.ParseInt(parts[2], 10, 64)
	value, err3 := strconv.Atoi(parts[4])
	if err1 != nil || err2 != nil || err3 != nil || id <= 0 {
		return answer("Неверная кнопка.")
	}
	r, err := readModulePanel(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return answer("Панель истекла. Откройте /modules.")
	}
	if err != nil {
		return err
	}
	ok := r.Owner == c.UserID && r.Destination == c.ChatID && r.Receipt > 0 && r.Receipt == c.MessageID
	if ok {
		ok, err = e.modulePanelAllowed(ctx, r)
		if err != nil {
			return err
		}
	}
	if err = e.audit(ctx, tx, u, c.UserID, ok); err != nil {
		return err
	}
	if !ok {
		return answer("Нет доступа или панель истекла. Откройте /modules.")
	}
	if revision != r.State.Revision {
		return answer("Кнопка устарела. Используйте обновлённую панель.")
	}
	p := &r.State
	p.Notice = ""
	specs := rules.Catalog()
	action := parts[3]
	if p.View == "chats" {
		if action != "chat" || value < 0 || value >= len(p.Chats) {
			return answer("Выберите чат.")
		}
		allowed, err := e.moduleChatAllowed(ctx, tx, c.UserID, p.Chats[value].ID)
		if err != nil {
			return err
		}
		if !allowed {
			return answer("Нет доступа к чату.")
		}
		if err = e.selectModuleChat(ctx, tx, p, p.Chats[value].ID); err != nil {
			return err
		}
	} else {
		switch action {
		case "m":
			if p.View != "list" || value < 0 || value >= len(specs) || value/notify.ModulesPerPage != p.Page {
				return answer("Модуль недоступен на этой странице.")
			}
			if err = e.selectModuleChat(ctx, tx, p, p.Origin); err != nil {
				return err
			}
			p.Selected = value
			p.View = "module"
		case "p":
			if p.View != "list" || value < 0 || value >= max(1, (len(specs)+notify.ModulesPerPage-1)/notify.ModulesPerPage) {
				return answer("Страница недоступна.")
			}
			p.Page = value
		case "params":
			if p.View != "module" || p.Selected < 0 || p.Selected >= len(specs) || value != 0 {
				return answer("Выберите настраиваемый модуль.")
			}
			p.View = "parameters"
		case "dict":
			if (p.View != "module" && p.View != "parameters") || p.Selected < 0 || p.Selected >= len(specs) {
				return answer("Выберите модуль.")
			}
			keys := make([]string, 0, len(specs[p.Selected].Matchers))
			for key := range specs[p.Selected].Matchers {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if value < 0 || value >= len(keys) {
				return answer("Словарь недоступен.")
			}
			p.Dictionary = keys[value]
			p.DictionaryPage = 0
			p.View = "dictionary"
		case "dp":
			if p.View != "dictionary" || p.Selected < 0 || p.Selected >= len(specs) {
				return answer("Выберите словарь.")
			}
			analyzer, err := e.compileModulePolicy(p.Origin, storage.ModuleSetting{Version: p.SettingVersion, Overrides: p.Draft})
			if err != nil {
				return err
			}
			terms := analyzer.Resolved().Options.Modules[specs[p.Selected].ID].Matchers[p.Dictionary].Terms
			if value < 0 || value >= max(1, (len(terms)+11)/12) {
				return answer("Страница недоступна.")
			}
			p.DictionaryPage = value
		case "back":
			switch p.View {
			case "dictionary", "parameters", "confirm":
				p.View = "module"
			default:
				if err = e.selectModuleChat(ctx, tx, p, p.Origin); err != nil {
					return err
				}
			}
		case "cancel":
			selected := p.Selected
			if err = e.selectModuleChat(ctx, tx, p, p.Origin); err != nil {
				return err
			}
			if selected >= 0 && selected < len(specs) {
				p.Selected = selected
				p.View = "module"
			}
			p.Notice = "Изменения отменены."
		case "save":
			if !p.Dirty || p.Selected < 0 || p.Selected >= len(specs) || specs[p.Selected].Required || (p.View != "module" && p.View != "parameters" && p.View != "dictionary") {
				return answer("Изменений нет.")
			}
			p.View = "confirm"
		case "apply":
			if p.View != "confirm" || !p.Dirty {
				return answer("Сначала сохраните и подтвердите изменения.")
			}
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(824916)`); err != nil {
				return err
			}
			current, err := storage.CurrentModules(ctx, tx, p.Origin)
			if err != nil {
				return err
			}
			if current.Version != p.SettingVersion {
				return answer("Настройки изменил другой администратор. Откройте /modules заново.")
			}
			candidate := storage.ModuleSetting{Version: current.Version + 1, Overrides: p.Draft}
			analyzer, err := e.compileModulePolicy(p.Origin, candidate)
			if err != nil {
				return answer("Недопустимые настройки. Изменения не применены.")
			}
			if err = e.registerAnalyzer(ctx, tx, analyzer, "telegram_module_settings"); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO chat_module_settings(chat_id,version,overrides) VALUES($1,$2,$3) ON CONFLICT(chat_id) DO UPDATE SET version=EXCLUDED.version,overrides=EXCLUDED.overrides,updated_at=now()`, p.Origin, candidate.Version, p.Draft); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO module_change_events(idempotency_key,chat_id,admin_user_id,version,old_overrides,new_overrides,ruleset_version) VALUES($1,$2,$3,$4,$5,$6,$7)`, fmt.Sprintf("modules:%s:%d", e.BotID, u.ID), p.Origin, c.UserID, candidate.Version, current.Overrides, p.Draft, analyzer.Resolved().Config.Version); err != nil {
				return err
			}
			p.SettingVersion = candidate.Version
			p.Dirty = false
			p.View = "list"
			p.Notice = "Сохранено. Действует для новых сообщений и правок."
		default:
			if (p.View != "module" && p.View != "parameters") || p.Selected < 0 || p.Selected >= len(specs) {
				return answer("Сначала выберите модуль.")
			}
			spec := specs[p.Selected]
			if spec.Required {
				return answer("Обязательные проверки доступны только для просмотра.")
			}
			draft := rules.MergeOptions(rules.Options{}, p.Draft)
			module := draft.Modules[spec.ID]
			if module.Thresholds == nil {
				module.Thresholds = map[string]int{}
			}
			switch action {
			case "mode":
				if value < 0 || value > 2 {
					return answer("Неверный режим.")
				}
				if value == 2 {
					if _, ok := spec.Thresholds["policy"]; !ok {
						return answer("Этот модуль только направляет на проверку.")
					}
				}
				enabled := value != 0
				module.Enabled = &enabled
				if _, ok := spec.Thresholds["policy"]; ok {
					module.Thresholds["policy"] = 0
					if value == 2 {
						module.Thresholds["policy"] = 1
					}
				}
			case "sens":
				module.Thresholds["sensitivity"] = value
			case "window":
				module.Thresholds["match_window_tokens"] = value
			case "cap":
				draft.FamilyCaps[spec.ID] = value
			case "reset":
				delete(draft.Modules, spec.ID)
				delete(draft.FamilyCaps, spec.ID)
			default:
				return answer("Неизвестная кнопка.")
			}
			if action != "reset" {
				draft.Modules[spec.ID] = module
			}
			if _, err = e.compileModulePolicy(p.Origin, storage.ModuleSetting{Version: p.SettingVersion + 1, Overrides: draft}); err != nil {
				return answer("Значение вне допустимого диапазона.")
			}
			p.Draft = draft
			p.Dirty = true
			p.Notice = "Черновик: нажмите «Сохранить»."
		}
	}
	if err = e.saveModulePanel(ctx, tx, id, &r); err != nil {
		return err
	}
	return answer("Панель обновлена.")
}

func (e *Engine) moduleEditCommand(ctx context.Context, tx pgx.Tx, u domain.Update, words bool, args []string) error {
	m := u.Message
	if len(args) < 3 {
		return e.ack(ctx, tx, *m, "Выберите модуль через /modules. Команда редактирует только черновик.")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return e.ack(ctx, tx, *m, "Неверный номер панели.")
	}
	r, err := readModulePanel(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return e.ack(ctx, tx, *m, "Откройте /modules заново.")
	}
	if err != nil {
		return err
	}
	ok := r.Owner == *m.UserID && r.Destination == m.ChatID && r.Receipt > 0
	if ok {
		ok, err = e.modulePanelAllowed(ctx, r)
		if err != nil {
			return err
		}
	}
	if err = e.audit(ctx, tx, u, *m.UserID, ok); err != nil {
		return err
	}
	if !ok {
		return e.ack(ctx, tx, *m, "Доступ запрещён или панель истекла.")
	}
	p := &r.State
	specs := rules.Catalog()
	if (p.View != "module" && p.View != "dictionary" && p.View != "parameters") || p.Selected < 0 || p.Selected >= len(specs) || specs[p.Selected].Required {
		return e.ack(ctx, tx, *m, "Выберите настраиваемый модуль.")
	}
	spec := specs[p.Selected]
	draft := rules.MergeOptions(rules.Options{}, p.Draft)
	module := draft.Modules[spec.ID]
	if words {
		if len(args) < 4 {
			return e.ack(ctx, tx, *m, "Нужны имя словаря, режим и слова через |.")
		}
		if _, ok := spec.Matchers[args[1]]; !ok {
			return e.ack(ctx, tx, *m, "Неизвестный словарь.")
		}
		terms := strings.Split(strings.Join(args[3:], " "), "|")
		for i := range terms {
			terms[i] = strings.TrimSpace(terms[i])
			if privacy.Redact(terms[i]) != terms[i] || strings.Contains(terms[i], "[REDACTED]") || strings.Contains(terms[i], "[PRIVATE]") {
				return e.ack(ctx, tx, *m, "Не добавляйте секреты или контакты в словари.")
			}
		}
		if module.Matchers == nil {
			module.Matchers = map[string]rules.MatcherConfig{}
		}
		module.Matchers[args[1]] = rules.MatcherConfig{Mode: args[2], Terms: terms}
	} else {
		value, err := strconv.Atoi(args[2])
		if err != nil || len(args) != 3 {
			return e.ack(ctx, tx, *m, "Нужен целочисленный порог.")
		}
		if _, ok := spec.Thresholds[args[1]]; !ok {
			return e.ack(ctx, tx, *m, "Неизвестный порог.")
		}
		if module.Thresholds == nil {
			module.Thresholds = map[string]int{}
		}
		module.Thresholds[args[1]] = value
	}
	draft.Modules[spec.ID] = module
	if _, err = e.compileModulePolicy(p.Origin, storage.ModuleSetting{Version: p.SettingVersion + 1, Overrides: draft}); err != nil {
		return e.ack(ctx, tx, *m, "Неверный режим, словарь или диапазон. Черновик не изменён.")
	}
	p.Draft = draft
	p.Dirty = true
	p.Notice = "Черновик изменён. Сохраните и подтвердите его."
	if err = e.saveModulePanel(ctx, tx, id, &r); err != nil {
		return err
	}
	return e.ack(ctx, tx, *m, "Черновик обновлён; действующие правила не изменены.")
}
