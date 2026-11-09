package notify

import (
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/privacy"
)

const ModelsPerPage = 6

type ModelPanel struct {
	Models         []string `json:"models"`
	Query          string   `json:"query"`
	Page           int      `json:"page"`
	Selected       int      `json:"selected"`
	Revision       int64    `json:"revision"`
	SettingVersion int64    `json:"setting_version"`
	Notice         string   `json:"notice,omitempty"`
}

func RenderModels(id int64, p ModelPanel, current string) (string, *models.InlineKeyboardMarkup) {
	esc := func(s string) string { return html.EscapeString(privacy.Redact(s)) }
	callback := func(action string, value int) string {
		return fmt.Sprintf("mdl:%d:%d:%s:%d", id, p.Revision, action, value)
	}
	kb := &models.InlineKeyboardMarkup{}
	text := "🤖 <b>Модель AI-проверки</b>\nТекущая: <code>" + esc(current) + "</code>\n"
	if p.Notice != "" {
		text += esc(p.Notice) + "\n"
	}
	if p.Selected >= 0 && p.Selected < len(p.Models) {
		text += "\nВыбрана: <code>" + esc(p.Models[p.Selected]) + "</code>\nПрименить для новых попыток, включая повторы pending-задач? Уже выполняющиеся запросы не прерываются.\n\nСмена не проверяет совместимость модели и не гарантирует бесплатность/subscription. Тарифы других моделей считаются неизвестными."
		kb.InlineKeyboard = [][]models.InlineKeyboardButton{{{Text: "✅ Применить", CallbackData: callback("a", p.Selected)}}, {{Text: "← Назад к списку", CallbackData: callback("p", p.Page)}}}
		return text, kb
	}
	pages := max(1, (len(p.Models)+ModelsPerPage-1)/ModelsPerPage)
	page := min(max(0, p.Page), pages-1)
	if p.Query != "" {
		text += "Поиск: <code>" + esc(p.Query) + "</code>\n"
	}
	text += fmt.Sprintf("\nСтраница %d/%d · моделей %d\n", page+1, pages, len(p.Models))
	for i := page * ModelsPerPage; i < min(len(p.Models), (page+1)*ModelsPerPage); i++ {
		label := p.Models[i]
		if len(label) > 60 {
			label = label[:28] + "…" + label[len(label)-28:]
		}
		if p.Models[i] == current {
			label = "✓ " + label
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, []models.InlineKeyboardButton{{Text: label, CallbackData: callback("s", i)}})
	}
	if len(p.Models) == 0 {
		text += "Ничего не найдено.\n"
	}
	row := []models.InlineKeyboardButton{}
	if page > 0 {
		row = append(row, models.InlineKeyboardButton{Text: "← Назад", CallbackData: callback("p", page-1)})
	}
	row = append(row, models.InlineKeyboardButton{Text: fmt.Sprintf("%d / %d", page+1, pages), CallbackData: callback("p", page)})
	if page+1 < pages {
		row = append(row, models.InlineKeyboardButton{Text: "Вперёд →", CallbackData: callback("p", page+1)})
	}
	kb.InlineKeyboard = append(kb.InlineKeyboard, row)
	text += "\nВыберите модель → подтвердите. Поиск: /model luna\nКаталог не гарантирует поддержку chat completions/JSON. При ошибке jobs остаются pending. Панель доступна автору 30 минут."
	return strings.TrimSpace(text), kb
}
