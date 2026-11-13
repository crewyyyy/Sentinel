package notify

import (
	"fmt"
	"html"
	"sort"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/privacy"
	"sentinel/internal/rules"
)

const ModulesPerPage = 6

type ModuleChat struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}
type ModulePanel struct {
	Dictionary     string        `json:"dictionary,omitempty"`
	DictionaryPage int           `json:"dictionary_page,omitempty"`
	Revision       int64         `json:"revision"`
	SettingVersion int64         `json:"setting_version"`
	BaseDigest     string        `json:"base_digest"`
	Chats          []ModuleChat  `json:"chats,omitempty"`
	Origin         int64         `json:"origin"`
	ChatTitle      string        `json:"chat_title"`
	View           string        `json:"view"`
	Page           int           `json:"page"`
	Selected       int           `json:"selected"`
	Draft          rules.Options `json:"draft"`
	Dirty          bool          `json:"dirty"`
	Notice         string        `json:"notice,omitempty"`
}

func ModuleMode(spec rules.Spec, settings rules.ModuleConfig) string {
	if spec.Required {
		return "Обязательный"
	}
	if settings.Enabled == nil || !*settings.Enabled {
		return "Выключен"
	}
	if settings.Thresholds["policy"] == 1 {
		return "Запрещено правилами"
	}
	return "На проверку"
}

func dictionaryTitle(key string) string {
	names := map[string]string{
		"intent": "Предложения", "subject": "Предмет предложения", "negation": "Отрицания",
		"solicitation": "Призывы", "question": "Вопросы", "reported": "Цитаты и жалобы",
		"promotion": "Реклама", "criticism": "Критика и цены", "request": "Просьбы",
		"secret": "Ключи и пароли", "query_keys": "Параметры ссылок", "bot_components": "Имена ботов",
	}
	if name, ok := names[key]; ok {
		return name
	}
	return key
}

func requiredModuleParameters(spec rules.Spec, policy rules.ResolvedSnapshot) string {
	text := "\n\n<b>Сигналы</b>"
	for _, signal := range spec.Signals {
		weight, ok := policy.Config.Weights[signal.Code]
		if !ok {
			weight = signal.DefaultWeight
		}
		text += fmt.Sprintf("\n%s: %+d", html.EscapeString(signalName(signal.Code)), weight)
		if signal.Mandatory {
			text += " · AI"
		}
	}
	keys := make([]string, 0, len(policy.Options.Modules[spec.ID].Thresholds))
	for key := range policy.Options.Modules[spec.ID].Thresholds {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		label := key
		if key == "url_scan_limit" {
			label = "Лимит ссылок"
		}
		text += fmt.Sprintf("\n%s: %d", html.EscapeString(label), policy.Options.Modules[spec.ID].Thresholds[key])
	}
	return text
}

func RenderModules(id int64, p ModulePanel, policy rules.ResolvedSnapshot) (string, *models.InlineKeyboardMarkup) {
	esc := func(s string) string { return html.EscapeString(privacy.Redact(s)) }
	kb := &models.InlineKeyboardMarkup{}
	add := func(label, action string, value int, icon, style string) models.InlineKeyboardButton {
		return button(label, fmt.Sprintf("mod:%d:%d:%s:%d", id, p.Revision, action, value), icon, style)
	}
	row := func(buttons ...models.InlineKeyboardButton) { kb.InlineKeyboard = append(kb.InlineKeyboard, buttons) }
	text := heading(iconShield, "🛡", "Модули")
	if p.View == "chats" {
		text += "\nВыберите чат."
		for i, chat := range p.Chats {
			row(add(privacy.Limit(chat.Title, 72), "chat", i, iconLink, ""))
		}
		return text, kb
	}
	text += "\n" + esc(privacy.Limit(p.ChatTitle, 100))
	specs := policy.Catalog
	selected := p.Selected >= 0 && p.Selected < len(specs)
	editing := selected && !specs[p.Selected].Required && (p.View == "module" || p.View == "parameters" || p.View == "dictionary" || p.View == "confirm")
	if p.Dirty && editing {
		text += "\n<i>Не сохранено.</i>"
	} else if p.Notice != "" {
		text += "\n" + esc(p.Notice)
	}
	if p.View == "dictionary" && selected {
		spec := specs[p.Selected]
		matcher := policy.Options.Modules[spec.ID].Matchers[p.Dictionary]
		pages := max(1, (len(matcher.Terms)+11)/12)
		page := min(max(0, p.DictionaryPage), pages-1)
		text += "\n\n<b>" + esc(spec.Title) + " · " + esc(dictionaryTitle(p.Dictionary)) + "</b>"
		text += fmt.Sprintf("\n%d/%d", page+1, pages)
		for i := page * 12; i < min(len(matcher.Terms), (page+1)*12); i++ {
			text += "\n<code>" + esc(matcher.Terms[i]) + "</code>"
		}
		if !spec.Required {
			text += fmt.Sprintf("\n\nЗаменить:\n<code>/modulewords %d %s %s слово | фраза</code>", id, esc(p.Dictionary), esc(matcher.Mode))
		}
		navigation := []models.InlineKeyboardButton{}
		if page > 0 {
			navigation = append(navigation, add("Назад", "dp", page-1, iconBack, ""))
		}
		if page+1 < pages {
			navigation = append(navigation, add("Далее", "dp", page+1, iconNext, ""))
		}
		if len(navigation) > 0 {
			row(navigation...)
		}
		row(add("К модулю", "back", 0, iconBack, ""))
		return text, kb
	}
	if p.View == "confirm" {
		text += "\n\n<b>Сохранить настройки?</b>"
		for _, spec := range specs {
			if spec.Required {
				continue
			}
			settings := policy.Options.Modules[spec.ID]
			text += "\n" + esc(spec.Title) + ": " + esc(ModuleMode(spec, settings))
			if sensitivity, ok := settings.Thresholds["sensitivity"]; ok {
				text += fmt.Sprintf(" · %d/3", sensitivity)
			}
			if override, ok := p.Draft.Modules[spec.ID]; ok {
				keys := make([]string, 0, len(override.Matchers))
				for key := range override.Matchers {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					text += fmt.Sprintf("\n  %s: %d слов/фраз", esc(dictionaryTitle(key)), len(settings.Matchers[key].Terms))
				}
				if _, changed := override.Thresholds["match_window_tokens"]; changed {
					text += fmt.Sprintf(" · расстояние %d", settings.Thresholds["match_window_tokens"])
				}
				thresholds := make([]string, 0, len(override.Thresholds))
				for key := range override.Thresholds {
					if key != "policy" && key != "sensitivity" && key != "match_window_tokens" {
						thresholds = append(thresholds, key)
					}
				}
				sort.Strings(thresholds)
				for _, key := range thresholds {
					text += fmt.Sprintf(" · %s=%d", esc(key), settings.Thresholds[key])
				}
			}
			if cap, changed := p.Draft.FamilyCaps[spec.ID]; changed {
				text += fmt.Sprintf(" · вес до %d", cap)
			}
		}
		row(add("Применить", "apply", 0, iconCheck, "success"), add("Назад", "back", 0, iconBack, ""))
		return text, kb
	}
	if (p.View == "module" || p.View == "parameters") && selected {
		spec := specs[p.Selected]
		settings := policy.Options.Modules[spec.ID]
		text += "\n\n<b>" + esc(spec.Title) + "</b>"
		if p.View == "parameters" && spec.Required {
			text += requiredModuleParameters(spec, policy)
			row(add("К модулю", "back", 0, iconBack, ""))
		} else if p.View == "parameters" {
			if window, ok := settings.Thresholds["match_window_tokens"]; ok {
				text += fmt.Sprintf("\nРасстояние между словами: %d", window)
				row(add("−2", "window", window-2, "", ""), add("+2", "window", window+2, "", ""))
			}
			if cap, ok := policy.Options.FamilyCaps[spec.ID]; ok {
				text += fmt.Sprintf("\nВклад в риск: до %d", cap)
			}
			row(add("Вес до 0", "cap", 0, "", ""), add("До 25", "cap", 25, "", ""), add("До 100", "cap", 100, "", ""))
			keys := make([]string, 0, len(settings.Thresholds))
			for key := range settings.Thresholds {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				text += fmt.Sprintf("\n<code>%s</code>: %d", esc(key), settings.Thresholds[key])
			}
			if len(keys) > 0 {
				text += fmt.Sprintf("\n\n<code>/modulelimit %d ИМЯ ЧИСЛО</code>", id)
			}
			row(add("Сбросить настройки", "reset", 0, iconBack, ""))
			row(add("К модулю", "back", 0, iconBack, ""))
		} else {
			text += "\n" + esc(spec.Description) + "\n\n" + esc(ModuleMode(spec, settings))
			if !spec.Required {
				modes := []models.InlineKeyboardButton{add("Выключить", "mode", 0, iconClose, ""), add("Проверять", "mode", 1, iconSearch, "primary")}
				if _, ok := spec.Thresholds["policy"]; ok {
					modes = append(modes, add("Запретить", "mode", 2, iconShield, "danger"))
				}
				row(modes...)
				if sensitivity, ok := settings.Thresholds["sensitivity"]; ok {
					text += fmt.Sprintf("\nЧувствительность: %d/3", sensitivity)
					row(add("Точная", "sens", 1, "", ""), add("Обычная", "sens", 2, "", ""), add("Широкая", "sens", 3, "", ""))
				}
			}
			keys := make([]string, 0, len(settings.Matchers))
			for key := range settings.Matchers {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for index, key := range keys {
				row(add(fmt.Sprintf("%s · %d", dictionaryTitle(key), len(settings.Matchers[key].Terms)), "dict", index, iconSearch, ""))
			}
			if spec.Required && len(settings.Matchers) == 0 && len(settings.Thresholds) == 0 {
				text += requiredModuleParameters(spec, policy)
			} else {
				row(add("Параметры", "params", 0, iconStats, ""))
			}
			row(add("К списку", "back", 0, iconBack, ""))
		}
	} else {
		pages := max(1, (len(specs)+ModulesPerPage-1)/ModulesPerPage)
		page := min(max(0, p.Page), pages-1)
		text += fmt.Sprintf("\n\n%d/%d", page+1, pages)
		for i := page * ModulesPerPage; i < min(len(specs), (page+1)*ModulesPerPage); i++ {
			spec := specs[i]
			row(add(spec.Title+" · "+ModuleMode(spec, policy.Options.Modules[spec.ID]), "m", i, iconSearch, ""))
		}
		navigation := []models.InlineKeyboardButton{}
		if page > 0 {
			navigation = append(navigation, add("Назад", "p", page-1, iconBack, ""))
		}
		if page+1 < pages {
			navigation = append(navigation, add("Далее", "p", page+1, iconNext, ""))
		}
		if len(navigation) > 0 {
			row(navigation...)
		}
	}
	if p.Dirty && editing {
		row(add("Сохранить", "save", 0, iconCheck, "success"), add("Отменить", "cancel", 0, iconClose, ""))
	}
	return text, kb
}
